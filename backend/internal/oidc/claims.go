package oidc

import (
	"encoding/json"
	"strings"
)

// IDClaims is the identity extracted from the ID token, already normalized: the
// rest of the backend does not know which claim was used for what.
type IDClaims struct {
	// Subject is the user's STABLE identifier at the provider, the one stored
	// in users.sso_subject. On Entra ID `oid` is preferred over `sub`: `sub`
	// differs per app registration and `oid` is the person's object id in the
	// directory. On every other provider it is `sub`.
	Subject string
	Email   string
	// Name is the display name ("Jane Doe"), which comes with the `profile`
	// scope. It is stored in users.display_name so as not to show the username
	// derived from the token, which is usually an email address.
	Name     string
	Username string
	Groups   []string
	// GroupsOverflowed is set when Entra ID cannot include the full list of
	// groups (more than ~200) and emits `_claim_names.groups` instead: they
	// have to be resolved against Microsoft Graph.
	GroupsOverflowed bool
}

// ExtractClaims builds IDClaims from the raw claims map. Empty usernameClaim
// and groupsClaim apply the defaults.
//
// Groups are accepted as an array of strings (the usual) or as a single
// comma/space-separated string (some IdPs map a multi-valued LDAP attribute
// that way), and are trimmed: an id with spaces would never match the mappings
// table and the failure would be invisible.
func ExtractClaims(raw map[string]json.RawMessage, subject, usernameClaim, groupsClaim string) IDClaims {
	c := IDClaims{Subject: subject}
	if oid := str(raw, "oid"); oid != "" {
		c.Subject = oid
	}
	c.Email = str(raw, "email")
	c.Name = str(raw, "name")

	if usernameClaim == "" {
		usernameClaim = "preferred_username"
	}
	c.Username = str(raw, usernameClaim)
	if c.Username == "" {
		c.Username = c.Email
	}
	if c.Username == "" {
		c.Username = c.Subject
	}

	if groupsClaim != "" {
		c.Groups = strList(raw, groupsClaim)
		if len(c.Groups) == 0 {
			var names map[string]json.RawMessage
			if v, ok := raw["_claim_names"]; ok && json.Unmarshal(v, &names) == nil {
				if _, overflowed := names[groupsClaim]; overflowed {
					c.GroupsOverflowed = true
				}
			}
		}
	}
	return c
}

func str(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func strList(raw map[string]json.RawMessage, key string) []string {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	var list []string
	if err := json.Unmarshal(v, &list); err != nil {
		var single string
		if err := json.Unmarshal(v, &single); err != nil {
			return nil
		}
		list = strings.FieldsFunc(single, func(r rune) bool { return r == ',' || r == ' ' })
	}
	out := make([]string, 0, len(list))
	for _, g := range list {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}
