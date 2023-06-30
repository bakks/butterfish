module github.com/bakks/butterfish

go 1.19

require (
	github.com/PullRequestInc/go-gpt3 v1.1.15
	github.com/alecthomas/kong v0.8.0
	github.com/charmbracelet/bubbles v0.16.1
	github.com/charmbracelet/bubbletea v0.24.2
	github.com/charmbracelet/lipgloss v0.7.1
	github.com/creack/pty v1.1.18
	github.com/drewlanenga/govector v0.0.0-20220726163947-b958ac08bc93
	github.com/golang/protobuf v1.5.3
	github.com/google/uuid v1.3.0
	github.com/joho/godotenv v1.5.1
	github.com/mitchellh/go-homedir v1.1.0
	github.com/mitchellh/go-ps v1.0.0
	github.com/muesli/reflow v0.3.0
	github.com/pkoukk/tiktoken-go v0.1.4
	github.com/sergi/go-diff v1.3.1
	github.com/spf13/afero v1.9.5
	github.com/stretchr/testify v1.8.2
	golang.org/x/term v0.9.0
	golang.org/x/tools v0.10.0
	google.golang.org/grpc v1.56.1
	google.golang.org/protobuf v1.31.0
	gopkg.in/yaml.v2 v2.4.0
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/bmizerany/assert v0.0.0-20160611221934-b7ed37b82869 // indirect
	github.com/containerd/console v1.0.4-0.20230313162750-1ae8d489ac81 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.14 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/termenv v0.15.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rivo/uniseg v0.4.4 // indirect
	golang.org/x/net v0.11.0 // indirect
	golang.org/x/sync v0.3.0 // indirect
	golang.org/x/sys v0.9.0 // indirect
	golang.org/x/text v0.10.0 // indirect
	google.golang.org/genproto v0.0.0-20230525234025-438c736192d0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230629202037-9506855d4529 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// replace tiktoken with local version
replace github.com/pkoukk/tiktoken-go => github.com/bakks/tiktoken-go v0.1.4-bakks
