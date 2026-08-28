package plugin

import (
	"encoding/json"
	"errors"
	"strings"
)

const supplyChainPayloadVersion = "weknora-plugin-signature-v1"

// SupplyChainSigningPayload is the canonical release identity signed by a
// publisher. Keep this format stable: pluginctl and plugin-runtime both use it.
func SupplyChainSigningPayload(
	pluginID, version, image, publisher, sourceRepository, imageDigest string,
) ([]byte, error) {
	values := []string{pluginID, version, image, publisher, imageDigest}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("signed plugin identity contains an empty required field")
		}
	}
	payload, err := json.Marshal(struct {
		Format           string `json:"format"`
		PluginID         string `json:"plugin_id"`
		Version          string `json:"version"`
		Image            string `json:"image"`
		Publisher        string `json:"publisher"`
		SourceRepository string `json:"source_repository"`
		ImageDigest      string `json:"image_digest"`
	}{
		Format:           supplyChainPayloadVersion,
		PluginID:         strings.TrimSpace(pluginID),
		Version:          strings.TrimPrefix(strings.TrimSpace(version), "v"),
		Image:            strings.TrimSpace(image),
		Publisher:        strings.TrimSpace(publisher),
		SourceRepository: strings.TrimSpace(sourceRepository),
		ImageDigest:      strings.TrimSpace(imageDigest),
	})
	if err != nil {
		return nil, err
	}
	return payload, nil
}
