package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

	_ "embed"
)

//go:embed countries.json
var embeddedCountriesData []byte

type currencyInfo struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
}

type languageInfo struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type countryInfo struct {
	Capital     string         `json:"capital"`
	CallingCode string         `json:"callingCode"`
	Currency    *currencyInfo  `json:"currency"`
	Languages   []languageInfo `json:"languages"`
	IsEU        bool           `json:"isEU"`
}

type countryDB struct {
	data atomic.Pointer[map[string]countryInfo]
}

var countryDBInstance = &countryDB{}

// Load embedded data at init so we have country info even before first refresh.
func init() {
	var m map[string]countryInfo
	if err := json.Unmarshal(embeddedCountriesData, &m); err == nil {
		countryDBInstance.data.Store(&m)
	}
}

const mledozeURL = "https://raw.githubusercontent.com/mledoze/countries/master/dist/countries.json"

// ponytail: EU members don't change often, hardcode is fine
var euCodes = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true,
	"DK": true, "EE": true, "FI": true, "FR": true, "DE": true, "GR": true,
	"HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true,
	"MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true,
	"SI": true, "ES": true, "SE": true,
}

func (db *countryDB) refresh() error {
	resp, err := http.Get(mledozeURL)
	if err != nil {
		return fmt.Errorf("country data download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("country data: HTTP %d", resp.StatusCode)
	}

	var raw []struct {
		CCA2        string   `json:"cca2"`
		Capital     []string `json:"capital"`
		CallingCode []string `json:"callingCode"`
		Currencies  map[string]struct {
			Name   string `json:"name"`
			Symbol string `json:"symbol"`
		} `json:"currencies"`
		Languages map[string]string `json:"languages"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("country data parse: %w", err)
	}

	m := make(map[string]countryInfo, len(raw))
	for _, c := range raw {
		if c.CCA2 == "" {
			continue
		}
		ci := countryInfo{IsEU: euCodes[c.CCA2]}
		if len(c.Capital) > 0 {
			ci.Capital = c.Capital[0]
		}
		if len(c.CallingCode) > 0 {
			ci.CallingCode = c.CallingCode[0]
		}
		for code, cur := range c.Currencies {
			cc := cur // copy
			ci.Currency = &currencyInfo{Code: code, Name: cc.Name, Symbol: cc.Symbol}
			break
		}
		if len(c.Languages) > 0 {
			for code, name := range c.Languages {
				ci.Languages = append(ci.Languages, languageInfo{Code: code, Name: name})
			}
		}
		if ci.Languages == nil {
			ci.Languages = []languageInfo{}
		}
		m[c.CCA2] = ci
	}

	db.data.Store(&m)
	return nil
}

func getCountryInfo(code string) *countryInfo {
	m := countryDBInstance.data.Load()
	if m == nil {
		return nil
	}
	if c, ok := (*m)[code]; ok {
		return &c
	}
	return nil
}
