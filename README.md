stew - Gitea Provisioning Tool
------------------------------

Stew is designed to aid testing tools against instances of Gitea.
It provides a declarative configuration format for initializing a fresh instance of Gitea, along with some provisional Git repos with state.

## Example

```yaml
url: "http://localhost:3000"
admin:
  username: flipt
  password: password
  email: dev@flipt.io
repositories:
- name: features
  contents:
  - path: local_dir_one
    message: "feat: commit one"
  - path: local_dir_two
    message: "feat: commit two"
  prs:
  - path: pr_dir_one
    message: "feat: commit three in PR"
  
```
