package bcadmin

import (
	"context"
	"strings"
)

// FamilyBusinessCentral is the application family of Business Central
// environments. Other families exist only for embedded (ISV) offerings and
// can be listed with [ApplicationsService.Families].
const FamilyBusinessCentral = "BusinessCentral"

// EnvironmentType is the type of an environment. The API uses different
// casing in different responses; compare with [EnvironmentType.Is].
type EnvironmentType string

// Environment types.
const (
	EnvironmentTypeProduction EnvironmentType = "Production"
	EnvironmentTypeSandbox    EnvironmentType = "Sandbox"
)

// Is reports whether t and other are the same type, ignoring case.
func (t EnvironmentType) Is(other EnvironmentType) bool {
	return strings.EqualFold(string(t), string(other))
}

// ApplicationsService lists the application families, countries, rings and
// versions environments can be created with. Use it through
// [Client.Applications].
type ApplicationsService struct {
	c *Client
}

// Applications returns the endpoints for available applications.
func (c *Client) Applications() *ApplicationsService { return &ApplicationsService{c: c} }

// ApplicationFamily is an application family with the countries and rings
// environments can be created in.
type ApplicationFamily struct {
	ApplicationFamily    string               `json:"applicationFamily"`
	CountriesRingDetails []CountryRingDetails `json:"countriesRingDetails"`
}

// CountryRingDetails lists the rings available in a country.
type CountryRingDetails struct {
	// CountryCode is the ISO 3166-1 alpha-2 country code, for example "DE".
	CountryCode string `json:"countryCode"`
	Rings       []Ring `json:"rings"`
}

// Ring is a release ring, for example the production ring "PROD" or the
// preview ring "PREVIEW".
type Ring struct {
	Name           string `json:"name"`
	ProductionRing bool   `json:"productionRing"`
	FriendlyName   string `json:"friendlyName"`
}

// Families returns the application families with their countries and rings.
//
// GET /applications/.
func (s *ApplicationsService) Families(ctx context.Context) ([]ApplicationFamily, error) {
	return listAll[ApplicationFamily](ctx, s.c, "applications/")
}

// Versions returns the application versions an environment of family can be
// created with in country and ring.
//
// GET /applications/{applicationFamily}/Countries/{countryCode}/Rings/{ringName}.
func (s *ApplicationsService) Versions(ctx context.Context, family, countryCode, ring string) ([]string, error) {
	return listAll[string](ctx, s.c, "applications/{}/Countries/{}/Rings/{}", family, countryCode, ring)
}
