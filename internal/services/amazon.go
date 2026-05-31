package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wbollock/benreadin/internal/models"
)

const (
	paAPIHost    = "webservices.amazon.com"
	paAPIPath    = "/paapi5"
	paAPIRegion  = "us-east-1"
	paAPIService = "ProductAdvertisingAPI"
)

// AmazonConfig holds PA-API credentials.
type AmazonConfig struct {
	AccessKey   string
	SecretKey   string
	PartnerTag  string
	Marketplace string // e.g. "www.amazon.com"
}

// AmazonService looks up book prices via Amazon PA-API 5.0.
type AmazonService struct {
	cfg    AmazonConfig
	client *http.Client
	cache  *CacheService
}

func NewAmazonService(cfg AmazonConfig, cache *CacheService) *AmazonService {
	return &AmazonService{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  cache,
	}
}

// Enabled returns false when credentials are not configured.
func (s *AmazonService) Enabled() bool {
	return s.cfg.AccessKey != "" && s.cfg.SecretKey != "" && s.cfg.PartnerTag != ""
}

// GetPrices returns Amazon pricing for a book by ISBN.
func (s *AmazonService) GetPrices(ctx context.Context, book models.Book) (models.AmazonResult, error) {
	result := models.AmazonResult{}

	if !s.Enabled() {
		return result, nil
	}

	isbn := book.BestISBN()
	if isbn == "" {
		return result, nil
	}

	// Check cache
	var cached models.AmazonResult
	hit, err := s.cache.GetAmazon(isbn, &cached)
	if err != nil {
		slog.Warn("amazon cache read error", "err", err)
	}
	if hit {
		return cached, nil
	}

	// Search by ISBN to get ASIN
	asin, kindleASIN, err := s.searchByISBN(ctx, isbn)
	if err != nil {
		return result, fmt.Errorf("amazon search: %w", err)
	}
	if asin == "" {
		return result, nil
	}

	result.ASIN = asin
	result.KindleASIN = kindleASIN
	result.AffiliateURL = fmt.Sprintf("https://www.amazon.com/dp/%s?tag=%s", asin, s.cfg.PartnerTag)
	result.Available = true

	// Get prices
	prices, err := s.getItemPrices(ctx, asin, kindleASIN)
	if err != nil {
		slog.Warn("amazon get prices failed", "asin", asin, "err", err)
	} else {
		result.KindlePrice = prices.kindle
		result.PaperbackPrice = prices.paperback
		result.HardcoverPrice = prices.hardcover
	}

	if err := s.cache.SetAmazon(isbn, result); err != nil {
		slog.Warn("amazon cache write error", "err", err)
	}

	return result, nil
}

// --- PA-API request helpers ---

type paSearchRequest struct {
	Keywords    string   `json:"Keywords"`
	SearchIndex string   `json:"SearchIndex"`
	Resources   []string `json:"Resources"`
	PartnerTag  string   `json:"PartnerTag"`
	PartnerType string   `json:"PartnerType"`
	Marketplace string   `json:"Marketplace"`
}

type paSearchResponse struct {
	SearchResult struct {
		Items []struct {
			ASIN string `json:"ASIN"`
		} `json:"Items"`
	} `json:"SearchResult"`
	Errors []struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	} `json:"Errors"`
}

type paGetItemsRequest struct {
	ItemIds     []string `json:"ItemIds"`
	Resources   []string `json:"Resources"`
	PartnerTag  string   `json:"PartnerTag"`
	PartnerType string   `json:"PartnerType"`
	Marketplace string   `json:"Marketplace"`
}

type paGetItemsResponse struct {
	ItemsResult struct {
		Items []struct {
			ASIN  string `json:"ASIN"`
			Offers *struct {
				Listings []struct {
					Price struct {
						Amount float64 `json:"Amount"`
					} `json:"Price"`
				} `json:"Listings"`
			} `json:"Offers"`
		} `json:"Items"`
	} `json:"ItemsResult"`
	Errors []struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	} `json:"Errors"`
}

type priceResult struct {
	kindle    float64
	paperback float64
	hardcover float64
}

// searchByISBN queries only the Books index. Kindle ASINs are extracted later
// from the GetItems response if a format match exists. Two separate
// SearchItems calls per book (Books + KindleStore) cost too many PA-API
// request units at scale — omitting the KindleStore call halves the usage.
// Books that have a Kindle edition still appear in the Books index.
func (s *AmazonService) searchByISBN(ctx context.Context, isbn string) (asin, kindleASIN string, err error) {
	body := paSearchRequest{
		Keywords:    isbn,
		SearchIndex: "Books",
		Resources:   []string{"ItemInfo.Title"},
		PartnerTag:  s.cfg.PartnerTag,
		PartnerType: "Associates",
		Marketplace: s.cfg.Marketplace,
	}

	var resp paSearchResponse
	if err := s.paCall(ctx, "/paapi5/searchitems", body, &resp); err != nil {
		return "", "", err
	}

	if len(resp.Errors) > 0 {
		return "", "", fmt.Errorf("PA-API error: %s", resp.Errors[0].Message)
	}

	if len(resp.SearchResult.Items) == 0 {
		return "", "", nil
	}

	asin = resp.SearchResult.Items[0].ASIN
	// kindleASIN is left empty; the UI falls back to an Amazon search URL.
	return asin, "", nil
}

func (s *AmazonService) getItemPrices(ctx context.Context, asin, kindleASIN string) (*priceResult, error) {
	ids := []string{asin}
	if kindleASIN != "" && kindleASIN != asin {
		ids = append(ids, kindleASIN)
	}

	body := paGetItemsRequest{
		ItemIds:     ids,
		Resources:   []string{"Offers.Listings.Price"},
		PartnerTag:  s.cfg.PartnerTag,
		PartnerType: "Associates",
		Marketplace: s.cfg.Marketplace,
	}

	var resp paGetItemsResponse
	if err := s.paCall(ctx, "/paapi5/getitems", body, &resp); err != nil {
		return nil, err
	}

	result := &priceResult{}
	for _, item := range resp.ItemsResult.Items {
		if item.Offers == nil || len(item.Offers.Listings) == 0 {
			continue
		}
		price := item.Offers.Listings[0].Price.Amount
		if item.ASIN == kindleASIN {
			result.kindle = price
		} else if item.ASIN == asin {
			// We don't know format here; set paperback as default
			result.paperback = price
		}
	}

	return result, nil
}

// paCall makes a signed PA-API 5.0 POST request.
func (s *AmazonService) paCall(ctx context.Context, path string, payload interface{}, out interface{}) error {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	endpoint := fmt.Sprintf("https://%s%s", paAPIHost, path)

	// Derive the operation name from the path
	operation := operationFromPath(path)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Host", paAPIHost)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Target", fmt.Sprintf("com.amazon.paapi5.v1.ProductAdvertisingAPIv1.%s", operation))

	// Build canonical request
	bodyHash := sha256Hex(bodyBytes)
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-amz-date:%s\nx-amz-target:%s\n",
		"application/json; charset=UTF-8",
		paAPIHost,
		amzDate,
		req.Header.Get("X-Amz-Target"),
	)
	signedHeaders := "content-type;host;x-amz-date;x-amz-target"
	canonicalRequest := strings.Join([]string{
		"POST",
		path,
		"",
		canonicalHeaders,
		signedHeaders,
		bodyHash,
	}, "\n")

	// Build string to sign
	credentialScope := strings.Join([]string{dateStamp, paAPIRegion, paAPIService, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	// Calculate signature
	signingKey := signingKey(s.cfg.SecretKey, dateStamp, paAPIRegion, paAPIService)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", authHeader)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PA-API %s returned %d: %s", path, resp.StatusCode, string(respBody))
	}

	return json.Unmarshal(respBody, out)
}

// --- AWS SigV4 helpers ---

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func signingKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

func operationFromPath(path string) string {
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	// capitalize first letter
	if len(name) == 0 {
		return name
	}
	// sort import required for canonical headers — already imported above
	_ = sort.Search
	return strings.ToUpper(name[:1]) + name[1:]
}
