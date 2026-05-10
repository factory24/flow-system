package models

type KeycloakUser struct {
	ID            string `json:"sub,omitempty"`
	GivenName     string `json:"given_name,omitempty"`
	Name          string `json:"name,omitempty"`
	FamilyName    string `json:"family_name,omitempty"`
	Username      string `json:"preferred_username,omitempty"`
	Email         string `json:"email,omitempty"`
	Audience      string `json:"audience"`
	EmailVerified bool   `json:"emailVerified,omitempty"`
	PhoneNumber   string `json:"phone_number,omitempty"`
	Country       string `json:"country"`
	Approved      bool   `json:"approved"`
	OrgID         string `json:"orgId"`
	RealmAccess   struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	BusinessId string `json:"businessId"`
	CreatedBy  string `json:"createdBy"`
	Role       string `json:"role"`
	RoleId     string `json:"roleId"`
	IsStaff    bool   `json:"isStaff"`
}
