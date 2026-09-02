package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"golang.org/x/sync/errgroup"
)

const (
	pluginMarketplaceTopic         = "weknora-plugin"
	pluginMarketplaceCacheTTL      = 10 * time.Minute
	pluginMarketplaceCacheMaxItems = 128
	pluginMarketplaceConcurrency   = 4
	pluginMarketplaceMaxPageSize   = 24
	// GitHub Search exposes at most 100 results per page and 1,000 results
	// for one query, so discovery is bounded to ten pages.
	pluginMarketplaceDiscoverySize    = 100
	pluginMarketplaceMaxSearchPages   = 10
	pluginMarketplaceMaxRepositories  = pluginMarketplaceDiscoverySize * pluginMarketplaceMaxSearchPages
	pluginMarketplaceMaxResponseBytes = 4 * 1024 * 1024
	githubAPIVersion                  = "2022-11-28"
)

// PluginMarketplaceQuery controls one GitHub-backed marketplace page.
type PluginMarketplaceQuery struct {
	Search         string
	Type           string
	Certification  string
	Sort           string
	CompatibleOnly bool
	Page           int
	PageSize       int
	Refresh        bool
}

// PluginMarketplaceResult contains only repositories whose root plugin.yaml
// passes the same SDK validation used by installation.
type PluginMarketplaceResult struct {
	Topic                 string                  `json:"topic"`
	Items                 []PluginMarketplaceItem `json:"items"`
	RepositoryCount       int                     `json:"repository_count"`
	GitHubRepositoryCount int                     `json:"github_repository_count"`
	SkippedCount          int                     `json:"skipped_count"`
	Page                  int                     `json:"page"`
	PageSize              int                     `json:"page_size"`
	RateLimitRemaining    int                     `json:"rate_limit_remaining,omitempty"`
	Stale                 bool                    `json:"stale"`
	CachedAt              time.Time               `json:"cached_at"`
}

// PluginMarketplaceItem joins verified manifest metadata with its GitHub
// repository. Installation still re-downloads and validates the manifest.
type PluginMarketplaceItem struct {
	ID                   string                        `json:"id"`
	Name                 string                        `json:"name"`
	Description          string                        `json:"description"`
	Version              string                        `json:"version"`
	ProtocolVersion      string                        `json:"protocol_version"`
	WeKnoraVersion       string                        `json:"weknora_version_constraint"`
	Image                string                        `json:"image"`
	Types                []string                      `json:"types"`
	Capabilities         []string                      `json:"capabilities"`
	Icon                 string                        `json:"icon"`
	ConfigSchema         map[string]any                `json:"config_schema"`
	SecretFields         []string                      `json:"secret_fields"`
	Permissions          pluginsdk.ManifestPermissions `json:"permissions"`
	SupplyChain          pluginsdk.ManifestSupplyChain `json:"supply_chain"`
	Resources            pluginsdk.ManifestResources   `json:"resources"`
	Repository           string                        `json:"repository"`
	RepositoryURL        string                        `json:"repository_url"`
	ManifestURL          string                        `json:"manifest_url"`
	Author               string                        `json:"author"`
	Stars                int                           `json:"stars"`
	UpdatedAt            time.Time                     `json:"updated_at"`
	Compatible           bool                          `json:"compatible"`
	CompatibilityMessage string                        `json:"compatibility_message,omitempty"`
	Certification        string                        `json:"certification"`
}

type githubPluginMarketplace struct {
	client   *http.Client
	token    string
	loadItem func(context.Context, string, githubRepository) (*PluginMarketplaceItem, error)

	mu               sync.RWMutex
	cache            map[string]pluginMarketplaceCacheEntry
	inventory        *pluginMarketplaceInventory
	inventoryExpires time.Time
	inventoryMu      sync.Mutex
	officialOwners   map[string]struct{}
	verifiedOwners   map[string]struct{}
}

type pluginMarketplaceCacheEntry struct {
	expiresAt time.Time
	result    *PluginMarketplaceResult
}

// pluginMarketplaceInventory is immutable after publication. Query-specific
// search, filtering, sorting and pagination operate on it without repeating
// GitHub search requests or manifest validation.
type pluginMarketplaceInventory struct {
	items                 []PluginMarketplaceItem
	githubRepositoryCount int
	skippedCount          int
	rateLimitRemaining    int
	cachedAt              time.Time
}

type githubRepositorySearchResponse struct {
	TotalCount int                `json:"total_count"`
	Items      []githubRepository `json:"items"`
}

type githubRepository struct {
	FullName      string    `json:"full_name"`
	HTMLURL       string    `json:"html_url"`
	DefaultBranch string    `json:"default_branch"`
	Stargazers    int       `json:"stargazers_count"`
	UpdatedAt     time.Time `json:"updated_at"`
	Topics        []string  `json:"topics"`
	Private       bool      `json:"private"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func newGitHubPluginMarketplace() *githubPluginMarketplace {
	return &githubPluginMarketplace{
		client:         newTrustedGitHubClient(githubAPIHost, "marketplace"),
		token:          strings.TrimSpace(os.Getenv("WEKNORA_PLUGIN_MARKET_GITHUB_TOKEN")),
		loadItem:       marketplaceItem,
		cache:          make(map[string]pluginMarketplaceCacheEntry),
		officialOwners: marketplaceOwners("WEKNORA_PLUGIN_MARKET_OFFICIAL_OWNERS", "Tencent"),
		verifiedOwners: marketplaceOwners("WEKNORA_PLUGIN_MARKET_VERIFIED_OWNERS", ""),
	}
}

func (s *PluginService) Marketplace(ctx context.Context, query PluginMarketplaceQuery) (*PluginMarketplaceResult, error) {
	if s.marketplace == nil {
		return nil, errors.New("plugin marketplace is unavailable")
	}
	return s.marketplace.list(ctx, s.hostVersion, query)
}

func (m *githubPluginMarketplace) list(
	ctx context.Context,
	hostVersion string,
	query PluginMarketplaceQuery,
) (*PluginMarketplaceResult, error) {
	query.Search = strings.TrimSpace(query.Search)
	query.Type = normalizeMarketplaceType(query.Type)
	query.Certification = strings.ToLower(strings.TrimSpace(query.Certification))
	query.Sort = strings.ToLower(strings.TrimSpace(query.Sort))
	if len(query.Search) > 80 {
		return nil, errors.New("plugin marketplace search must not exceed 80 characters")
	}
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 12
	}
	if query.PageSize > pluginMarketplaceMaxPageSize {
		query.PageSize = pluginMarketplaceMaxPageSize
	}
	if query.Sort == "" {
		query.Sort = "updated"
	}
	if query.Sort != "updated" && query.Sort != "stars" && query.Sort != "name" {
		return nil, errors.New("plugin marketplace sort must be updated, stars or name")
	}
	if query.Certification != "" && query.Certification != "official" && query.Certification != "verified" && query.Certification != "community" {
		return nil, errors.New("plugin marketplace certification is invalid")
	}

	cacheKey := fmt.Sprintf("%s:%s:%s:%s:%t:%d:%d", strings.ToLower(query.Search), query.Type, query.Certification, query.Sort, query.CompatibleOnly, query.Page, query.PageSize)
	if !query.Refresh {
		if cached := m.cached(cacheKey); cached != nil {
			return cached, nil
		}
	}

	inventory, inventoryStale, err := m.loadInventory(ctx, hostVersion, query.Refresh)
	if err != nil {
		if cached := m.stale(cacheKey); cached != nil {
			cached.Stale = true
			return cached, nil
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	validItems := make([]PluginMarketplaceItem, 0, len(inventory.items))
	for _, item := range inventory.items {
		if !marketplaceItemMatches(item, query) {
			continue
		}
		validItems = append(validItems, item)
	}
	sortMarketplaceItems(validItems, query.Sort)
	result := &PluginMarketplaceResult{
		Topic:                 pluginMarketplaceTopic,
		Items:                 marketplacePage(validItems, query.Page, query.PageSize),
		RepositoryCount:       len(validItems),
		GitHubRepositoryCount: inventory.githubRepositoryCount,
		SkippedCount:          inventory.skippedCount,
		Page:                  query.Page,
		PageSize:              query.PageSize,
		RateLimitRemaining:    inventory.rateLimitRemaining,
		Stale:                 inventoryStale,
		CachedAt:              inventory.cachedAt,
	}
	if !inventoryStale {
		m.store(cacheKey, result)
	}
	return cloneMarketplaceResult(result), nil
}

func (m *githubPluginMarketplace) loadInventory(
	ctx context.Context,
	hostVersion string,
	refresh bool,
) (*pluginMarketplaceInventory, bool, error) {
	if !refresh {
		if cached := m.cachedInventory(false); cached != nil {
			return cached, false, nil
		}
	}

	// Only one caller performs the potentially expensive GitHub discovery and
	// manifest validation. Other callers re-check the cache after entering.
	m.inventoryMu.Lock()
	defer m.inventoryMu.Unlock()
	if !refresh {
		if cached := m.cachedInventory(false); cached != nil {
			return cached, false, nil
		}
	}

	inventory, err := m.buildInventory(ctx, hostVersion)
	if err != nil {
		if stale := m.cachedInventory(true); stale != nil {
			return stale, true, nil
		}
		return nil, false, err
	}
	m.storeInventory(inventory)
	return inventory, false, nil
}

func (m *githubPluginMarketplace) buildInventory(
	ctx context.Context,
	hostVersion string,
) (*pluginMarketplaceInventory, error) {
	repositories, total, remaining, err := m.searchRepositories(ctx, PluginMarketplaceQuery{
		Page:     1,
		PageSize: pluginMarketplaceDiscoverySize,
	})
	if err != nil {
		return nil, err
	}

	items := make([]*PluginMarketplaceItem, len(repositories))
	loadItem := m.loadItem
	if loadItem == nil {
		loadItem = marketplaceItem
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(pluginMarketplaceConcurrency)
	for index := range repositories {
		index := index
		group.Go(func() error {
			item, itemErr := loadItem(groupCtx, hostVersion, repositories[index])
			if itemErr == nil && item != nil {
				item.Certification = m.certification(repositories[index].Owner.Login)
				items[index] = item
			}
			return nil
		})
	}
	_ = group.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	validItems := make([]PluginMarketplaceItem, 0, len(items))
	skipped := 0
	for _, item := range items {
		if item == nil {
			skipped++
			continue
		}
		validItems = append(validItems, *item)
	}
	return &pluginMarketplaceInventory{
		items:                 validItems,
		githubRepositoryCount: total,
		skippedCount:          skipped,
		rateLimitRemaining:    remaining,
		cachedAt:              time.Now().UTC(),
	}, nil
}

func (m *githubPluginMarketplace) searchRepositories(
	ctx context.Context,
	query PluginMarketplaceQuery,
) ([]githubRepository, int, int, error) {
	query.Page = 1
	query.PageSize = pluginMarketplaceDiscoverySize
	repositories := make([]githubRepository, 0, pluginMarketplaceDiscoverySize)
	seen := make(map[string]struct{}, pluginMarketplaceMaxRepositories)
	total := 0
	remaining := 0
	hasRemaining := false

	for page := 1; page <= pluginMarketplaceMaxSearchPages; page++ {
		query.Page = page
		pageItems, pageTotal, pageRemaining, pageHasRemaining, err := m.searchRepositoryPage(ctx, query)
		if err != nil {
			return nil, 0, 0, err
		}
		if page == 1 {
			total = max(0, pageTotal)
		}
		if pageHasRemaining && (!hasRemaining || pageRemaining < remaining) {
			remaining = pageRemaining
			hasRemaining = true
		}
		for _, repository := range pageItems {
			key := strings.ToLower(strings.TrimSpace(repository.FullName))
			if key != "" {
				if _, duplicate := seen[key]; duplicate {
					continue
				}
				seen[key] = struct{}{}
			}
			repositories = append(repositories, repository)
			if len(repositories) == pluginMarketplaceMaxRepositories {
				return repositories, total, remaining, nil
			}
		}

		// GitHub can report a moving total while repositories are updated. A
		// short page is the most reliable end marker; the first-page total is
		// only used to avoid one unnecessary request when the last page is full.
		if len(pageItems) < query.PageSize || (total > 0 && page*query.PageSize >= min(total, pluginMarketplaceMaxRepositories)) {
			break
		}
	}
	return repositories, total, remaining, nil
}

func (m *githubPluginMarketplace) searchRepositoryPage(
	ctx context.Context,
	query PluginMarketplaceQuery,
) ([]githubRepository, int, int, bool, error) {
	search := "topic:" + pluginMarketplaceTopic + " archived:false fork:false"
	values := url.Values{
		"q":        []string{search},
		"sort":     []string{"updated"},
		"order":    []string{"desc"},
		"page":     []string{strconv.Itoa(query.Page)},
		"per_page": []string{strconv.Itoa(query.PageSize)},
	}
	endpoint := "https://" + githubAPIHost + "/search/repositories?" + values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("create GitHub marketplace request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "WeKnora-Plugin-Marketplace")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if m.token != "" {
		request.Header.Set("Authorization", "Bearer "+m.token)
	}

	response, err := m.client.Do(request)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("search GitHub plugin repositories: %w", err)
	}
	defer response.Body.Close()
	remaining, hasRemaining := parseGitHubRateLimitRemaining(response.Header)
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		if response.StatusCode == http.StatusForbidden && hasRemaining && remaining == 0 {
			return nil, 0, 0, true, errors.New("GitHub marketplace rate limit exceeded; configure WEKNORA_PLUGIN_MARKET_GITHUB_TOKEN")
		}
		return nil, 0, remaining, hasRemaining, fmt.Errorf("GitHub marketplace returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, pluginMarketplaceMaxResponseBytes+1))
	if err != nil {
		return nil, 0, remaining, hasRemaining, fmt.Errorf("read GitHub marketplace response: %w", err)
	}
	if len(body) > pluginMarketplaceMaxResponseBytes {
		return nil, 0, remaining, hasRemaining, fmt.Errorf("GitHub marketplace response exceeds %d bytes", pluginMarketplaceMaxResponseBytes)
	}
	var payload githubRepositorySearchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0, remaining, hasRemaining, fmt.Errorf("decode GitHub marketplace response: %w", err)
	}
	return payload.Items, payload.TotalCount, remaining, hasRemaining, nil
}

func parseGitHubRateLimitRemaining(header http.Header) (int, bool) {
	raw := strings.TrimSpace(header.Get("X-RateLimit-Remaining"))
	if raw == "" {
		return 0, false
	}
	remaining, err := strconv.Atoi(raw)
	if err != nil || remaining < 0 {
		return 0, false
	}
	return remaining, true
}

func marketplaceItem(ctx context.Context, hostVersion string, repository githubRepository) (*PluginMarketplaceItem, error) {
	if repository.FullName == "" || repository.DefaultBranch == "" || repository.Owner.Login == "" {
		return nil, errors.New("GitHub repository metadata is incomplete")
	}
	if repository.Private || repository.Archived || repository.Fork || !containsMarketplaceTopic(repository.Topics) {
		return nil, errors.New("GitHub repository does not meet marketplace discovery rules")
	}
	manifestURL := fmt.Sprintf(
		"https://github.com/%s/blob/%s/plugin.yaml",
		repository.FullName,
		url.PathEscape(repository.DefaultBranch),
	)
	input := PluginInstallInput{ManifestURL: manifestURL}
	if err := resolvePluginManifest(ctx, &input); err != nil {
		return nil, err
	}
	manifest, err := pluginsdk.ParseManifest(input.ManifestYAML)
	if err != nil {
		return nil, err
	}
	compatible := true
	compatibilityMessage := ""
	if err := validateHostVersion(hostVersion, manifest.Spec.WeKnoraVersionConstraint); err != nil {
		compatible = false
		compatibilityMessage = err.Error()
	}
	permissions := manifest.Spec.Permissions
	permissions.AllowedHosts = append(make([]string, 0, len(permissions.AllowedHosts)), permissions.AllowedHosts...)
	permissions.DataAccess = append(make([]string, 0, len(permissions.DataAccess)), permissions.DataAccess...)
	return &PluginMarketplaceItem{
		ID:                   manifest.Metadata.ID,
		Name:                 manifest.Metadata.Name,
		Description:          manifest.Metadata.Description,
		Version:              manifest.Metadata.Version,
		ProtocolVersion:      manifest.Spec.ProtocolVersion,
		WeKnoraVersion:       manifest.Spec.WeKnoraVersionConstraint,
		Image:                manifest.Spec.Image,
		Types:                append(make([]string, 0, len(manifest.Spec.Types)), manifest.Spec.Types...),
		Capabilities:         append(make([]string, 0, len(manifest.Spec.Capabilities)), manifest.Spec.Capabilities...),
		Icon:                 manifest.Spec.Icon,
		ConfigSchema:         manifest.Spec.Config.Schema,
		SecretFields:         append(make([]string, 0, len(manifest.Spec.Config.SecretFields)), manifest.Spec.Config.SecretFields...),
		Permissions:          permissions,
		SupplyChain:          manifest.Spec.SupplyChain,
		Resources:            manifest.Spec.Resources,
		Repository:           repository.FullName,
		RepositoryURL:        repository.HTMLURL,
		ManifestURL:          manifestURL,
		Author:               repository.Owner.Login,
		Stars:                repository.Stargazers,
		UpdatedAt:            repository.UpdatedAt,
		Compatible:           compatible,
		CompatibilityMessage: compatibilityMessage,
	}, nil
}

func containsMarketplaceTopic(values []string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), pluginMarketplaceTopic) {
			return true
		}
	}
	return false
}

func (m *githubPluginMarketplace) cached(key string) *PluginMarketplaceResult {
	m.mu.RLock()
	entry, ok := m.cache[key]
	m.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	return cloneMarketplaceResult(entry.result)
}

func (m *githubPluginMarketplace) stale(key string) *PluginMarketplaceResult {
	m.mu.RLock()
	entry, ok := m.cache[key]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return cloneMarketplaceResult(entry.result)
}

func (m *githubPluginMarketplace) cachedInventory(allowExpired bool) *pluginMarketplaceInventory {
	m.mu.RLock()
	inventory := m.inventory
	expiresAt := m.inventoryExpires
	m.mu.RUnlock()
	if inventory == nil || (!allowExpired && time.Now().After(expiresAt)) {
		return nil
	}
	return inventory
}

func (m *githubPluginMarketplace) storeInventory(inventory *pluginMarketplaceInventory) {
	m.mu.Lock()
	m.inventory = inventory
	m.inventoryExpires = time.Now().Add(pluginMarketplaceCacheTTL)
	// Every query-specific page belongs to the previous inventory snapshot.
	// Clear them atomically so a successful manual refresh cannot serve a page
	// generated from older repository metadata.
	m.cache = make(map[string]pluginMarketplaceCacheEntry)
	m.mu.Unlock()
}

func (m *githubPluginMarketplace) store(key string, result *PluginMarketplaceResult) {
	m.mu.Lock()
	if m.cache == nil {
		m.cache = make(map[string]pluginMarketplaceCacheEntry)
	}
	now := time.Now()
	for cachedKey, entry := range m.cache {
		if now.After(entry.expiresAt) {
			delete(m.cache, cachedKey)
		}
	}
	if _, exists := m.cache[key]; !exists && len(m.cache) >= pluginMarketplaceCacheMaxItems {
		oldestKey := ""
		var oldestExpiry time.Time
		for cachedKey, entry := range m.cache {
			if oldestKey == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = cachedKey
				oldestExpiry = entry.expiresAt
			}
		}
		delete(m.cache, oldestKey)
	}
	m.cache[key] = pluginMarketplaceCacheEntry{
		expiresAt: now.Add(pluginMarketplaceCacheTTL),
		result:    cloneMarketplaceResult(result),
	}
	m.mu.Unlock()
}

func normalizeMarketplaceType(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}

func marketplaceItemMatches(item PluginMarketplaceItem, query PluginMarketplaceQuery) bool {
	if query.Search != "" {
		searchable := strings.ToLower(strings.Join([]string{
			item.ID, item.Name, item.Description, item.Repository, item.Author,
		}, "\n"))
		for _, term := range strings.Fields(strings.ToLower(query.Search)) {
			if !strings.Contains(searchable, term) {
				return false
			}
		}
	}
	if query.Type != "" {
		matched := false
		for _, value := range item.Types {
			if normalizeMarketplaceType(value) == query.Type {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if query.CompatibleOnly && !item.Compatible {
		return false
	}
	return query.Certification == "" || item.Certification == query.Certification
}

func sortMarketplaceItems(items []PluginMarketplaceItem, order string) {
	sort.SliceStable(items, func(i, j int) bool {
		switch order {
		case "stars":
			if items[i].Stars != items[j].Stars {
				return items[i].Stars > items[j].Stars
			}
		case "name":
			return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
		default:
			if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				return items[i].UpdatedAt.After(items[j].UpdatedAt)
			}
		}
		return items[i].ID < items[j].ID
	})
}

func marketplacePage(items []PluginMarketplaceItem, page, pageSize int) []PluginMarketplaceItem {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || len(items) == 0 {
		return []PluginMarketplaceItem{}
	}
	pageOffset := page - 1
	if pageOffset > len(items)/pageSize {
		return []PluginMarketplaceItem{}
	}
	start := pageOffset * pageSize
	if start >= len(items) {
		return []PluginMarketplaceItem{}
	}
	count := min(pageSize, len(items)-start)
	return append([]PluginMarketplaceItem(nil), items[start:start+count]...)
}

func marketplaceOwners(envName, fallback string) map[string]struct{} {
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		value = fallback
	}
	result := make(map[string]struct{})
	for _, owner := range strings.Split(value, ",") {
		if owner = strings.ToLower(strings.TrimSpace(owner)); owner != "" {
			result[owner] = struct{}{}
		}
	}
	return result
}

func (m *githubPluginMarketplace) certification(owner string) string {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if _, ok := m.officialOwners[owner]; ok {
		return "official"
	}
	if _, ok := m.verifiedOwners[owner]; ok {
		return "verified"
	}
	return "community"
}

func cloneMarketplaceResult(result *PluginMarketplaceResult) *PluginMarketplaceResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.Items = make([]PluginMarketplaceItem, len(result.Items))
	for index := range result.Items {
		cloned.Items[index] = cloneMarketplaceItem(result.Items[index])
	}
	return &cloned
}

func cloneMarketplaceItem(item PluginMarketplaceItem) PluginMarketplaceItem {
	cloned := item
	cloned.Types = append([]string(nil), item.Types...)
	cloned.Capabilities = append([]string(nil), item.Capabilities...)
	cloned.SecretFields = append([]string(nil), item.SecretFields...)
	cloned.Permissions.AllowedHosts = append([]string(nil), item.Permissions.AllowedHosts...)
	cloned.Permissions.DataAccess = append([]string(nil), item.Permissions.DataAccess...)
	cloned.ConfigSchema = cloneMarketplaceSchema(item.ConfigSchema)
	return cloned
}

func cloneMarketplaceSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	cloned := make(map[string]any, len(schema))
	for key, value := range schema {
		cloned[key] = cloneMarketplaceSchemaValue(value)
	}
	return cloned
}

func cloneMarketplaceSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMarketplaceSchema(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index := range typed {
			cloned[index] = cloneMarketplaceSchemaValue(typed[index])
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
