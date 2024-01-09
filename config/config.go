package config

type Config struct {
	URL   string `json:"url"`
	Admin struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	} `json:"admin"`
	Repositories []Repository `json:"repositories"`
}

type Content struct {
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Repository struct {
	Name     string    `json:"name"`
	Contents []Content `json:"contents"`
	PRs      []Content `json:"prs"`
}
