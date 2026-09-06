package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Tencent/WeKnora/internal/plugindev"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(os.Args[2:])
	case "validate":
		err = runValidate(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "compat":
		err = runCompat(os.Args[2:])
	case "sign":
		err = runSign(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pluginctl:", err)
		os.Exit(1)
	}
}

func runInit(args []string) error {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	pluginType := set.String("type", "", "data_source, document_parser, web_search, model_provider or retrieval_engine")
	id := set.String("id", "", "reverse-DNS plugin ID")
	name := set.String("name", "", "display name")
	output := set.String("output", "", "empty output directory")
	image := set.String("image", "", "container image")
	sdkPath := set.String("sdk-path", ".", "path to the WeKnora repository during local development")
	if err := set.Parse(args); err != nil {
		return err
	}
	if err := plugindev.Scaffold(plugindev.ScaffoldOptions{Output: *output, Type: *pluginType, ID: *id, Name: *name, Image: *image, SDKPath: *sdkPath}); err != nil {
		return err
	}
	fmt.Printf("created %s plugin scaffold at %s\n", *pluginType, *output)
	return nil
}

func runValidate(args []string) error {
	set := flag.NewFlagSet("validate", flag.ContinueOnError)
	manifestPath := set.String("manifest", "plugin.yaml", "manifest path")
	if err := set.Parse(args); err != nil {
		return err
	}
	manifest, err := plugindev.ValidateManifest(*manifestPath)
	if err != nil {
		return err
	}
	fmt.Printf("valid plugin manifest: id=%s version=%s types=%v\n", manifest.Metadata.ID, manifest.Metadata.Version, manifest.NormalizedTypes())
	return nil
}

func runDoctor(args []string) error {
	set := flag.NewFlagSet("doctor", flag.ContinueOnError)
	address := set.String("address", "127.0.0.1:9000", "local plugin address")
	manifestPath := set.String("manifest", "plugin.yaml", "manifest path")
	configPath := set.String("config", "", "optional JSON config path")
	if err := set.Parse(args); err != nil {
		return err
	}
	result, err := plugindev.Doctor(context.Background(), *address, *manifestPath, *configPath)
	if err != nil {
		return err
	}
	fmt.Printf("plugin=%s protocol=%s health=%s config=%s message=%q\n", result.PluginID, result.ProtocolVersion, result.HealthStatus, result.ConfigValidation, result.HealthMessage)
	return nil
}

func runCompat(args []string) error {
	set := flag.NewFlagSet("compat", flag.ContinueOnError)
	manifestPath := set.String("manifest", "plugin.yaml", "manifest path")
	weknoraVersion := set.String("weknora-version", "", "current WeKnora semantic version")
	address := set.String("address", "", "optional local plugin address for live checks")
	configPath := set.String("config", "", "optional JSON config path for live checks")
	if err := set.Parse(args); err != nil {
		return err
	}
	result, err := plugindev.CheckCompatibility(context.Background(), plugindev.CompatibilityOptions{
		ManifestPath:   *manifestPath,
		WeKnoraVersion: *weknoraVersion,
		Address:        *address,
		ConfigPath:     *configPath,
	})
	if err != nil {
		return err
	}
	fmt.Printf(
		"compatible plugin: id=%s version=%s weknora=%s protocol=%s types=%s",
		result.PluginID,
		result.PluginVersion,
		result.WeKnoraVersion,
		result.ProtocolVersion,
		strings.Join(result.Types, ","),
	)
	if !result.LiveChecked {
		fmt.Println(" live=skipped")
		return nil
	}
	fmt.Printf(
		" live=passed health=%s services=%s\n",
		result.HealthStatus,
		strings.Join(result.Services, ","),
	)
	return nil
}

func runSign(args []string) error {
	set := flag.NewFlagSet("sign", flag.ContinueOnError)
	manifestPath := set.String("manifest", "plugin.yaml", "manifest path")
	privateKeyPath := set.String("private-key", "", "Ed25519 PKCS#8 PEM private key path")
	if err := set.Parse(args); err != nil {
		return err
	}
	result, err := plugindev.SignManifest(*manifestPath, *privateKeyPath)
	if err != nil {
		return err
	}
	fmt.Printf("signature=%s\npublic_key=%s\n", result.Signature, result.PublicKey)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: pluginctl <init|validate|doctor|compat|sign> [options]")
}
