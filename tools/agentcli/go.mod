module github.com/mediusfy/modulex/tools/agentcli

go 1.25.0

require (
	github.com/mediusfy/modulex v0.0.0
	github.com/mediusfy/modulex/tools/modboundary v0.0.0
	github.com/mediusfy/modulex/tools/scaffold v0.0.0
	golang.org/x/tools v0.48.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
)

replace github.com/mediusfy/modulex => ../..

replace github.com/mediusfy/modulex/tools/modboundary => ../modboundary

replace github.com/mediusfy/modulex/tools/scaffold => ../scaffold
