package models

// AmazonResult holds pricing data from Amazon PA-API for a single book.
type AmazonResult struct {
	ASIN          string  `json:"asin"`
	KindleASIN    string  `json:"kindle_asin,omitempty"`
	KindlePrice   float64 `json:"kindle_price,omitempty"`
	PaperbackPrice float64 `json:"paperback_price,omitempty"`
	HardcoverPrice float64 `json:"hardcover_price,omitempty"`
	AffiliateURL  string  `json:"affiliate_url,omitempty"`
	Available     bool    `json:"available"`
}
