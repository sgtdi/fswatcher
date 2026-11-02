module fswatcher-version-compatibility

go 1.27.3

require (
	github.com/sgtdi/fswatcher v1.3.0
	github.com/sgtdi/fswatcher/v2 v2.0.0
)

require golang.org/x/sys v0.49.0 // indirect

replace github.com/sgtdi/fswatcher/v2 => ../..
