package gitprovider

func init() {
	conformCases = append(conformCases, conformCase{
		typ: "github", token: "good", user: "mistergregg", query: "unire",
		handler: requireHeader("Authorization", "Bearer good", jsonRoutes(map[string]string{
			"/api/v3/user": `{"login":"mistergregg"}`,
			"/api/v3/user/repos": `[{"full_name":"mistergregg/unire","description":"agents","clone_url":"https://github.com/mistergregg/unire.git","default_branch":"main"},
			                         {"full_name":"mistergregg/other","description":"x","clone_url":"https://github.com/mistergregg/other.git","default_branch":"main"}]`,
		})),
	})
}
