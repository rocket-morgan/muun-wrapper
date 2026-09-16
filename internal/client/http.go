package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/wapu/muun-wrapper/internal/wallet"
)

// Client calls the wrapper REST API using its static bearer token.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// New validates the API endpoint and creates a client for it.
func New(rawURL, token string) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("API token is required")
	}
	baseURL, err := url.Parse(rawURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid API URL %q", rawURL)
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/"
	return &Client{baseURL: baseURL, token: token, httpClient: http.DefaultClient}, nil
}

// CreateWallet creates the wallet managed by the API and returns its first receive address.
func (c *Client) CreateWallet(ctx context.Context) (wallet.WalletInfo, error) {
	return c.doWalletInfo(ctx, http.MethodPost, "wallet/create")
}

// ReceiveAddress reserves and returns the next on-chain receive address.
func (c *Client) ReceiveAddress(ctx context.Context) (wallet.WalletInfo, error) {
	return c.doWalletInfo(ctx, http.MethodGet, "receive/address")
}

// Balance returns the current spendable wallet balance in satoshis.
func (c *Client) Balance(ctx context.Context) (wallet.Balance, error) {
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "balance"})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return wallet.Balance{}, fmt.Errorf("build API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return wallet.Balance{}, fmt.Errorf("call wrapper API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return wallet.Balance{}, fmt.Errorf("wrapper API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var balance wallet.Balance
	if err := json.NewDecoder(response.Body).Decode(&balance); err != nil {
		return wallet.Balance{}, fmt.Errorf("decode API response: %w", err)
	}
	return balance, nil
}

// Operations returns the current operation history from the wrapper API.
func (c *Client) Operations(ctx context.Context) ([]wallet.Operation, error) {
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "operations"})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call wrapper API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf("wrapper API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var operations []wallet.Operation
	if err := json.NewDecoder(response.Body).Decode(&operations); err != nil {
		return nil, fmt.Errorf("decode API response: %w", err)
	}
	return operations, nil
}

// ReceiveInvoice creates a BOLT11 payment request for an incoming Lightning payment.
func (c *Client) ReceiveInvoice(ctx context.Context, amountSatoshis *int64) (wallet.Invoice, error) {
	payload, err := json.Marshal(struct {
		AmountSatoshis *int64 `json:"amountSatoshis"`
	}{AmountSatoshis: amountSatoshis})
	if err != nil {
		return wallet.Invoice{}, fmt.Errorf("encode invoice request: %w", err)
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "receive/invoice"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return wallet.Invoice{}, fmt.Errorf("build API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return wallet.Invoice{}, fmt.Errorf("call wrapper API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return wallet.Invoice{}, fmt.Errorf("wrapper API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var invoice wallet.Invoice
	if err := json.NewDecoder(response.Body).Decode(&invoice); err != nil {
		return wallet.Invoice{}, fmt.Errorf("decode API response: %w", err)
	}
	return invoice, nil
}

// Send submits an on-chain payment through the wrapper API.
func (c *Client) Send(ctx context.Context, input wallet.SendRequest) (wallet.SendResult, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return wallet.SendResult{}, fmt.Errorf("encode send request: %w", err)
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "send"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return wallet.SendResult{}, fmt.Errorf("build API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return wallet.SendResult{}, fmt.Errorf("call wrapper API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return wallet.SendResult{}, fmt.Errorf("wrapper API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var result wallet.SendResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return wallet.SendResult{}, fmt.Errorf("decode API response: %w", err)
	}
	return result, nil
}

// Sync fulfills unfulfilled incoming Lightning swaps known to the wrapper.
func (c *Client) Sync(ctx context.Context) (wallet.SyncResult, error) {
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: "sync"})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return wallet.SyncResult{}, fmt.Errorf("build API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return wallet.SyncResult{}, fmt.Errorf("call wrapper API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return wallet.SyncResult{}, fmt.Errorf("wrapper API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var result wallet.SyncResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return wallet.SyncResult{}, fmt.Errorf("decode API response: %w", err)
	}
	return result, nil
}

func (c *Client) doWalletInfo(ctx context.Context, method, path string) (wallet.WalletInfo, error) {
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return wallet.WalletInfo{}, fmt.Errorf("build API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return wallet.WalletInfo{}, fmt.Errorf("call wrapper API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return wallet.WalletInfo{}, fmt.Errorf("wrapper API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var info wallet.WalletInfo
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		return wallet.WalletInfo{}, fmt.Errorf("decode API response: %w", err)
	}
	return info, nil
}
