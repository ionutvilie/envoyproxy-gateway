package cmd

import (
	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/envoyproxy/gateway/internal/envoygateway/config"
	gatewayapirunner "github.com/envoyproxy/gateway/internal/gatewayapi/runner"
	infrarunner "github.com/envoyproxy/gateway/internal/infrastructure/runner"
	"github.com/envoyproxy/gateway/internal/message"
	providerrunner "github.com/envoyproxy/gateway/internal/provider/runner"
	xdsserverrunner "github.com/envoyproxy/gateway/internal/xds/server/runner"
	xdstranslatorrunner "github.com/envoyproxy/gateway/internal/xds/translator/runner"
)

var (
	// cfgPath is the path to the EnvoyGateway configuration file.
	cfgPath string
)

// getServerCommand returns the server cobra command to be executed.
func getServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "server",
		Aliases: []string{"serve"},
		Short:   "Serve Envoy Gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			return server()
		},
	}
	cmd.PersistentFlags().StringVarP(&cfgPath, "config-path", "c", "",
		"The path to the configuration file.")

	return cmd
}

// server serves Envoy Gateway.
func server() error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}

	if err := setupRunners(cfg); err != nil {
		return err
	}

	return nil
}

// getConfig gets the Server configuration
func getConfig() (*config.Server, error) {
	// Initialize with default config parameters.
	cfg, err := config.NewDefaultServer()
	if err != nil {
		return nil, err
	}
	log := cfg.Logger

	// Read the config file.
	if cfgPath == "" {
		// Use default config parameters
		log.Info("No config file provided, using default parameters")
	} else {
		// Load the config file.
		eg, err := config.Decode(cfgPath)
		if err != nil {
			log.Error(err, "failed to decode config file", "name", cfgPath)
			return nil, err
		}
		// Set defaults for unset fields
		eg.SetDefaults()
		cfg.EnvoyGateway = eg
	}
	return cfg, nil
}

// setupRunners starts all the runners required for the Envoy Gateway to
// fulfill its tasks.
func setupRunners(cfg *config.Server) error {
	// TODO - Setup a Config Manager
	// https://github.com/envoyproxy/gateway/issues/43
	ctx := ctrl.SetupSignalHandler()

	pResources := new(message.ProviderResources)
	// Start the Provider Service
	// It fetches the resources from the configured provider type
	// and publishes it
	providerRunner := providerrunner.New(&providerrunner.Config{
		Server:            *cfg,
		ProviderResources: pResources,
	})
	if err := providerRunner.Start(ctx); err != nil {
		return err
	}

	xdsIR := new(message.XdsIR)
	infraIR := new(message.InfraIR)
	// Start the GatewayAPI Translator Runner
	// It subscribes to the provider resources, translates it to xDS IR
	// and infra IR resources and publishes them.
	gwRunner := gatewayapirunner.New(&gatewayapirunner.Config{
		Server:            *cfg,
		ProviderResources: pResources,
		XdsIR:             xdsIR,
		InfraIR:           infraIR,
	})
	if err := gwRunner.Start(ctx); err != nil {
		return err
	}

	xResources := new(message.XdsResources)
	// Start the Xds Translator Service
	// It subscribes to the xdsIR, translates it into xds Resources and publishes it.
	xdsTranslatorRunner := xdstranslatorrunner.New(&xdstranslatorrunner.Config{
		Server:       *cfg,
		XdsIR:        xdsIR,
		XdsResources: xResources,
	})
	if err := xdsTranslatorRunner.Start(ctx); err != nil {
		return err
	}

	// Start the Infra Manager Runner
	// It subscribes to the infraIR, translates it into Envoy Proxy infrastructure
	// resources such as K8s deployment and services.
	infraRunner := infrarunner.New(&infrarunner.Config{
		Server:  *cfg,
		InfraIR: infraIR,
	})
	if err := infraRunner.Start(ctx); err != nil {
		return err
	}

	// Start the xDS Server
	// It subscribes to the xds Resources and configures the remote Envoy Proxy
	// via the xDS Protocol
	xdsServerRunner := xdsserverrunner.New(&xdsserverrunner.Config{
		Server:       *cfg,
		XdsResources: xResources,
	})
	if err := xdsServerRunner.Start(ctx); err != nil {
		return err
	}

	// Wait until done
	<-ctx.Done()
	// Close messages
	pResources.GatewayClasses.Close()
	pResources.Gateways.Close()
	pResources.HTTPRoutes.Close()
	xdsIR.Close()
	infraIR.Close()
	xResources.Close()

	cfg.Logger.Info("shutting down")

	return nil
}