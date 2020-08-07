package saml

import (
	"encoding/xml"
	"fmt"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
)

const timeFormat = "2006-01-02T15:04:05.999Z07:00"

// SAML LogoutRequest and related methods
// Reference: https://github.com/crewjam/saml/blob/v0.3.1/service_provider.go
type ServiceProvider struct {
	*saml.ServiceProvider
}

// LogoutRequest  represents the SAML object of the same name, a request from an IDP
// to destroy a user's session.
//
// See http://docs.oasis-open.org/security/saml/v2.0/saml-core-2.0-os.pdf
type LogoutRequest struct {
	XMLName xml.Name `xml:"urn:oasis:names:tc:SAML:2.0:protocol LogoutRequest"`

	ID           string       `xml:",attr"`
	Version      string       `xml:",attr"`
	IssueInstant time.Time    `xml:",attr"`
	Destination  string       `xml:",attr"`
	Issuer       *saml.Issuer `xml:"urn:oasis:names:tc:SAML:2.0:assertion Issuer"`
	NameID       *saml.NameID
	Signature    *etree.Element

	SessionIndex string `xml:",attr"`
}

// MakeRedirectLogoutRequest creates a SAML authentication request using
// the HTTP-Redirect binding. It returns a URL that we will redirect the user to
// in order to start the auth process.
func (sp *ServiceProvider) MakeRedirectLogoutRequest(nameID string) (*LogoutRequest, error) {
	return sp.MakeLogoutRequest(sp.GetSLOBindingLocation(saml.HTTPRedirectBinding), nameID)
}

// GetSLOBindingLocation returns URL for the IDP's Single Log Out Service binding
// of the specified type (HTTPRedirectBinding or HTTPPostBinding)
func (sp *ServiceProvider) GetSLOBindingLocation(binding string) string {
	for _, idpSSODescriptor := range sp.IDPMetadata.IDPSSODescriptors {
		for _, singleLogoutService := range idpSSODescriptor.SingleLogoutServices {
			if singleLogoutService.Binding == binding {
				return singleLogoutService.Location
			}
		}
	}
	return ""
}

// MakeLogoutRequest produces a new LogoutRequest object for idpURL.
func (sp *ServiceProvider) MakeLogoutRequest(idpURL, nameID string) (*LogoutRequest, error) {

	req := LogoutRequest{
		ID:           fmt.Sprintf("id-%x", randomBytes(20)),
		IssueInstant: saml.TimeNow(),
		Version:      "2.0",
		Destination:  idpURL,
		Issuer: &saml.Issuer{
			Format: "urn:oasis:names:tc:SAML:2.0:nameid-format:entity",
			Value:  sp.MetadataURL.String(),
		},
		NameID: &saml.NameID{
			Format:          sp.nameIDFormat(),
			Value:           nameID,
			NameQualifier:   sp.IDPMetadata.EntityID,
			SPNameQualifier: sp.MetadataURL.String(),
		},
	}
	return &req, nil
}

// Element returns an etree.Element representing the object in XML form.
func (r *LogoutRequest) Element() *etree.Element {
	el := etree.NewElement("samlp:LogoutRequest")
	el.CreateAttr("xmlns:saml", "urn:oasis:names:tc:SAML:2.0:assertion")
	el.CreateAttr("xmlns:samlp", "urn:oasis:names:tc:SAML:2.0:protocol")
	el.CreateAttr("ID", r.ID)
	el.CreateAttr("Version", r.Version)
	el.CreateAttr("IssueInstant", r.IssueInstant.Format(timeFormat))
	if r.Destination != "" {
		el.CreateAttr("Destination", r.Destination)
	}
	if r.Issuer != nil {
		el.AddChild(r.Issuer.Element())
	}
	if r.NameID != nil {
		el.AddChild(r.NameID.Element())
	}
	if r.Signature != nil {
		el.AddChild(r.Signature)
	}
	if r.SessionIndex != "" {
		el.CreateAttr("SessionIndex", r.SessionIndex)
	}
	return el
}

func (sp *ServiceProvider) nameIDFormat() string {
	var nameIDFormat string
	switch sp.AuthnNameIDFormat {
	case "":
		// To maintain library back-compat, use "transient" if unset.
		nameIDFormat = string(saml.TransientNameIDFormat)
	case saml.UnspecifiedNameIDFormat:
		// Spec defines an empty value as "unspecified" so don't set one.
	default:
		nameIDFormat = string(sp.AuthnNameIDFormat)
	}
	return nameIDFormat
}
