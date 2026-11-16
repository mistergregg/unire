# Adding a git provider

A provider teaches Unire how to talk to one kind of git server (list repos, check a token). Adding one is a single Go file plus a test file. You do not touch the UI: the "add server" form is built from the provider's `Info`.

## 1. Write `internal/gitprovider/<name>.go`

```go
package gitprovider

type myserver struct{ c *client }

func init() {
	Register(Info{
		Type:            "myserver",              // stable id, stored in config
		Label:           "My Server",             // shown in the UI
		BaseURLRequired: true,                    // false if there is a default host
		BaseURLHint:     "e.g. https://git.example.com",
		TokenHelp:       "Where to create the token and which scopes it needs.",
	}, func(cfg Config) (Provider, error) {
		return myserver{newClient(cfg, cfg.BaseURL+"/api/v1", http.Header{
			"Authorization": {"Bearer " + cfg.Token},
		})}, nil
	})
}

func (p myserver) TestConnection(ctx context.Context) (string, error) { /* return the user name */ }
func (p myserver) ListRepos(ctx context.Context, query string) ([]Repo, error) { /* filter by query */ }
```

`newClient` and `getJSON` handle timeouts, self-signed certificates (`InsecureSkipVerify`), and turn 401/403/404 into friendly errors, so most providers are about 40 lines. Never put the token in an error message or log line.

## 2. Write `internal/gitprovider/<name>_test.go`

Add a fake server and register it for the shared contract:

```go
func init() {
	conformCases = append(conformCases, conformCase{
		typ: "myserver", token: "good", user: "greg", query: "api",
		handler: requireHeader("Authorization", "Bearer good", jsonRoutes(map[string]string{
			"/api/v1/user":  `{"login":"greg"}`,
			"/api/v1/repos": `[...]`,
		})),
	})
}
```

`TestConformance` then checks that a good token connects, repos come back with a name and clone URL, and a bad token gives a "rejected the token" error. `TestEveryProviderHasConformanceCase` fails if you forget this step.

## 3. Run

```bash
go test ./internal/gitprovider
```

That's all. The provider shows up in the server list and the repo search once built.

## Notes

- Clone and push use the same token over HTTPS through a git credential helper, so the provider only needs an API client for listing and testing.
- Servers with no API at all (plain `git://` or SSH) will get a generic provider later; it will implement only cloning.
