package serve

import (
	"context"
	"embed"
	"io/fs"
	"net/http"

	"github.com/MakeNowJust/heredoc/v2"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

//go:embed static
var static embed.FS

type ServeOptions struct {
	FailureData string
}

func (opts *ServeOptions) Run(ctx context.Context) error {
	root, err := fs.Sub(static, "static")
	if err != nil {
		return err
	}

	http.Handle("/", http.FileServer(http.FS(root)))
	http.HandleFunc("/failure_data.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=120")
		http.ServeFile(w, r, opts.FailureData)
	})
	klog.Info("Listening http://localhost:8080...")
	return http.ListenAndServe(":8080", nil)
}

func NewCmdServe() *cobra.Command {
	opts := &ServeOptions{}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start an HTTP server",
		Long: heredoc.Doc(`
			Start an HTTP server with a failure viewer.
		`),
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			err := opts.Run(cmd.Context())
			if err != nil {
				klog.Exit(err)
			}
		},
	}

	cmd.Flags().StringVar(&opts.FailureData, "failure_data", "./failure_data.json", "file with failures data")

	return cmd
}
