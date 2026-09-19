package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	libwallet "github.com/muun/libwallet"
	"github.com/wapu/muun-wrapper/internal/api"
	"github.com/wapu/muun-wrapper/internal/client"
	"github.com/wapu/muun-wrapper/internal/wallet"
)

const (
	defaultAPIURL     = "http://127.0.0.1:8080"
	defaultHoustonURL = "https://pub.api.muun.io/houston/"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: muun-wrapper <server|create|address|balance|operations|invoice|invoice-status|send|sync|recovery-code|recovery-setup|emergency-kit>")
	}

	switch args[0] {
	case "server":
		return runServer()
	case "recovery-code":
		_, err := fmt.Fprintln(output, libwallet.GenerateRecoveryCode())
		return err
	case "create", "address", "balance", "operations", "invoice", "invoice-status", "send", "sync", "recovery-setup", "emergency-kit":
		return runClientCommand(args[0], args[1:], output)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runClientCommand(command string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apiURL := flags.String("api-url", envOrDefault("MUUN_WRAPPER_API_URL", defaultAPIURL), "wrapper API URL")
	apiToken := flags.String("api-token", os.Getenv("MUUN_WRAPPER_API_TOKEN"), "wrapper API token")
	amountSatoshis := flags.Int64("amount-sats", 0, "amount for a Lightning invoice in satoshis")
	destination := flags.String("destination", "", "Bitcoin payment destination")
	description := flags.String("description", "", "payment description")
	recoveryCodeFile := flags.String("recovery-code-file", "", "path to a recovery code file")
	paymentHash := flags.String("payment-hash", "", "payment hash of an issued Lightning invoice")
	if err := flags.Parse(args); err != nil {
		return err
	}

	apiClient, err := client.New(*apiURL, *apiToken)
	if err != nil {
		return err
	}
	ctx := context.Background()
	var result any
	switch command {
	case "create":
		result, err = apiClient.CreateWallet(ctx)
	case "address":
		result, err = apiClient.ReceiveAddress(ctx)
	case "balance":
		result, err = apiClient.Balance(ctx)
	case "operations":
		result, err = apiClient.Operations(ctx)
	case "invoice":
		if *amountSatoshis <= 0 {
			return errors.New("invoice requires --amount-sats greater than zero")
		}
		result, err = apiClient.ReceiveInvoice(ctx, amountSatoshis)
	case "invoice-status":
		if *paymentHash == "" {
			return errors.New("invoice-status requires --payment-hash")
		}
		result, err = apiClient.InvoiceStatus(ctx, *paymentHash)
	case "send":
		if strings.TrimSpace(*destination) == "" {
			return errors.New("send requires --destination")
		}
		if *amountSatoshis <= 0 {
			return errors.New("send requires --amount-sats greater than zero")
		}
		result, err = apiClient.Send(ctx, wallet.SendRequest{
			Destination:    *destination,
			AmountSatoshis: *amountSatoshis,
			Description:    *description,
		})
	case "sync":
		result, err = apiClient.Sync(ctx)
	case "recovery-setup":
		if strings.TrimSpace(*recoveryCodeFile) == "" {
			return errors.New("recovery-setup requires --recovery-code-file")
		}
		recoveryCode, readErr := os.ReadFile(*recoveryCodeFile)
		if readErr != nil {
			return fmt.Errorf("read recovery code file: %w", readErr)
		}
		if strings.TrimSpace(string(recoveryCode)) == "" {
			return errors.New("recovery code file is empty")
		}
		result, err = apiClient.SetupRecovery(ctx, strings.TrimSpace(string(recoveryCode)))
	case "emergency-kit":
		result, err = apiClient.EmergencyKit(ctx)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func runServer() error {
	passphrase := os.Getenv("MUUN_WRAPPER_PASSPHRASE")
	if passphrase == "" {
		return errors.New("MUUN_WRAPPER_PASSPHRASE is required")
	}
	apiToken := os.Getenv("MUUN_WRAPPER_API_TOKEN")
	if apiToken == "" {
		return errors.New("MUUN_WRAPPER_API_TOKEN is required")
	}

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           envOrDefault("MUUN_WRAPPER_DATA_DIR", "/data"),
		Passphrase:        passphrase,
		HoustonURL:        envOrDefault("MUUN_WRAPPER_HOUSTON_URL", defaultHoustonURL),
		PrimaryCurrency:   envOrDefault("MUUN_WRAPPER_PRIMARY_CURRENCY", "USD"),
		ClientVersion:     19020002,
		ClientVersionName: "2.16.0",
	})
	if err != nil {
		return err
	}
	syncEvery, err := syncInterval()
	if err != nil {
		return err
	}
	pollContext, cancelPoll := context.WithCancel(context.Background())
	defer cancelPoll()
	manager.RunPoller(pollContext, syncEvery, func(err error) {
		log.Printf("wallet sync failed: %v", err)
	})

	server := &http.Server{
		Addr:              envOrDefault("MUUN_WRAPPER_LISTEN_ADDR", ":8080"),
		Handler:           api.NewServer(manager, apiToken),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func syncInterval() (time.Duration, error) {
	interval, err := time.ParseDuration(envOrDefault("MUUN_WRAPPER_SYNC_INTERVAL", "20s"))
	if err != nil || interval <= 0 {
		return 0, errors.New("MUUN_WRAPPER_SYNC_INTERVAL must be a positive duration")
	}
	return interval, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
