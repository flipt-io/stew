package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	"code.gitea.io/sdk/gitea"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/peterbourgon/ff/v3"
	"golang.org/x/exp/slog"
	"gopkg.in/yaml.v2"
)

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
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Repository struct {
	Name     string    `json:"name"`
	Contents []Content `json:"contents"`
	PRs      []Content `json:"prs"`
}

func fatalOnError(err error) {
	if err != nil {
		slog.Error("Exiting...", "error", err)
		os.Exit(1)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	set := flag.NewFlagSet("stew", flag.ContinueOnError)
	var (
		configPath = set.String("config", "stew.yaml", "Path to stew config file")
	)

	fatalOnError(ff.Parse(set, os.Args[1:],
		ff.WithEnvVarPrefix("STEW"),
		ff.WithConfigFile("config"),
	))

	fi, err := os.Open(*configPath)
	fatalOnError(err)

	var conf Config
	fatalOnError(yaml.NewDecoder(fi).Decode(&conf))

	if conf.URL == "" {
		fatalOnError(errors.New("Must supply Gitea URL"))
	}

	slog.Info("Configuring gitea", "address", conf.URL)

	// provision empty target gitea instance
	fatalOnError(setupGitea(conf))

	// ensure we can connect to gitea
	cli, err := giteaClient(conf)
	fatalOnError(err)

	for _, repository := range conf.Repositories {
		// create configured repository
		_, err = createRepo(cli, repository.Name)
		fatalOnError(err)

		workdir := memfs.New()
		repo, err := git.InitWithOptions(memory.NewStorage(), workdir, git.InitOptions{
			DefaultBranch: "main",
		})
		fatalOnError(err)

		repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{fmt.Sprintf("%s/%s/%s.git", conf.URL, conf.Admin.Username, repository.Name)},
		})

		hash := plumbing.ZeroHash
		for _, content := range repository.Contents {
			commit, err := copyAndPush(conf, repo, hash, "main", content.Message, os.DirFS(content.Path))
			fatalOnError(err)

			hash = commit
		}

		for _, content := range repository.PRs {
			branch := path.Dir(content.Path)
			_, err := copyAndPush(conf, repo, hash, branch, content.Message, os.DirFS(content.Path))
			fatalOnError(err)

			_, _, err = cli.CreatePullRequest(conf.Admin.Username, repository.Name, gitea.CreatePullRequestOption{
				Head:  branch,
				Base:  "main",
				Title: content.Message,
				Body:  content.Message,
			})
			fatalOnError(err)
		}
	}
}

func setupGitea(cfg Config) error {
	for i := 0; true; i++ {
		_, err := http.Get(cfg.URL)
		if err == nil {
			break
		}

		if i < 20 {
			time.Sleep(time.Second)
			continue
		}

		return fmt.Errorf("cannot connect to gitea: %w", err)
	}

	val, err := url.ParseQuery(giteaSetupForm)
	if err != nil {
		return err
	}

	val.Set("admin_name", cfg.Admin.Username)
	val.Set("admin_passwd", cfg.Admin.Password)
	val.Set("admin_confirm_passwd", cfg.Admin.Password)
	val.Set("admin_email", cfg.Admin.Email)

	resp, err := http.PostForm(cfg.URL, val)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	return nil
}

func giteaClient(cfg Config) (cli *gitea.Client, err error) {
	for i := 0; i < 20; i++ {
		cli, err = gitea.NewClient(cfg.URL, gitea.SetBasicAuth(cfg.Admin.Username, cfg.Admin.Password))
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
	}

	if cli == nil {
		return nil, errors.New("couldn't connect to gitea")
	}

	return cli, nil
}

func createRepo(cli *gitea.Client, repository string) (*gitea.Repository, error) {
	origin, _, err := cli.CreateRepo(gitea.CreateRepoOption{
		Name:          repository,
		DefaultBranch: "main",
	})

	return origin, err
}

func copyAndPush(conf Config, repo *git.Repository, hash plumbing.Hash, branch, message string, src fs.FS) (plumbing.Hash, error) {
	tree, err := repo.Worktree()
	if err != nil {
		return plumbing.ZeroHash, err
	}

	// checkout branch if not main from provided hash
	if hash != plumbing.ZeroHash && branch != "main" {
		if err := repo.CreateBranch(&config.Branch{
			Name: branch,
		}); err != nil {
			return plumbing.ZeroHash, err
		}

		if err := tree.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(branch),
			Hash:   hash,
			Create: true,
			Force:  true,
		}); err != nil {
			return plumbing.ZeroHash, err
		}
	}

	err = fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return tree.Filesystem.MkdirAll(path, 0755)
		}

		contents, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}

		target, err := filepath.Rel(branch, path)
		if err != nil {
			return err
		}

		fi, err := tree.Filesystem.Create(target)
		if err != nil {
			return err
		}

		_, err = fi.Write(contents)
		if err != nil {
			return err
		}

		return fi.Close()
	})
	if err != nil {
		return plumbing.ZeroHash, err
	}

	err = tree.AddWithOptions(&git.AddOptions{All: true})
	if err != nil {
		return plumbing.ZeroHash, err
	}

	commit, err := tree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Email: conf.Admin.Email, Name: "dev"},
	})
	if err != nil {
		return plumbing.ZeroHash, err
	}

	fmt.Fprintln(os.Stderr, "Pushing", commit)
	if err := repo.Push(&git.PushOptions{
		Auth:       &githttp.BasicAuth{Username: conf.Admin.Username, Password: conf.Admin.Password},
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("%s:refs/heads/%s", branch, branch)),
			config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch)),
		},
	}); err != nil {
		return plumbing.ZeroHash, err
	}

	return commit, nil
}

const giteaSetupForm = "db_type=sqlite3&db_host=localhost%3A3306&db_user=root&db_passwd=&db_name=gitea&ssl_mode=disable&db_schema=&charset=utf8&db_path=%2Fdata%2Fgitea%2Fgitea.db&app_name=Gitea%3A+Git+with+a+cup+of+tea&repo_root_path=%2Fdata%2Fgit%2Frepositories&lfs_root_path=%2Fdata%2Fgit%2Flfs&run_user=git&domain=localhost&ssh_port=22&http_port=3000&app_url=http%3A%2F%2Flocalhost%3A3000%2F&log_root_path=%2Fdata%2Fgitea%2Flog&smtp_addr=&smtp_port=&smtp_from=&smtp_user=&smtp_passwd=&enable_federated_avatar=on&enable_open_id_sign_in=on&enable_open_id_sign_up=on&default_allow_create_organization=on&default_enable_timetracking=on&no_reply_address=noreply.localhost&password_algorithm=pbkdf2&admin_email="
