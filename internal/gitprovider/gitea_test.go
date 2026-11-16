package gitprovider

func init() {
	h := requireHeader("Authorization", "token good", jsonRoutes(map[string]string{
		"/api/v1/user":         `{"login":"greg"}`,
		"/api/v1/repos/search": `{"data":[{"full_name":"greg/api","description":"d","clone_url":"https://g/greg/api.git","default_branch":"main","private":true}]}`,
	}))
	conformCases = append(conformCases,
		conformCase{typ: "gitea", handler: h, token: "good", user: "greg", query: "api"},
		conformCase{typ: "forgejo", handler: h, token: "good", user: "greg", query: "api"},
	)
}
