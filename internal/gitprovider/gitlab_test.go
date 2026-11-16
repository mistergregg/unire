package gitprovider

func init() {
	conformCases = append(conformCases, conformCase{
		typ: "gitlab", token: "good", user: "greg", query: "api",
		handler: requireHeader("PRIVATE-TOKEN", "good", jsonRoutes(map[string]string{
			"/api/v4/user":     `{"username":"greg"}`,
			"/api/v4/projects": `[{"path_with_namespace":"greg/api","description":"d","http_url_to_repo":"https://gl/greg/api.git","default_branch":"main","visibility":"private"}]`,
		})),
	})
}
