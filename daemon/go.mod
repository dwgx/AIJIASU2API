module ajsaccountd

go 1.26.10

require ajsproxy v0.0.0

require (
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.49.0 // indirect
)

replace ajsproxy => ../emulator
