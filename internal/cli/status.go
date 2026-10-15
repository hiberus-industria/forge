package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Estado del daemon: sesión, sincronización, captura y tarea forzada",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			st, err := e.daemon.Status(cmd.Context())
			if err != nil {
				return err
			}
			e.o.title("forge " + st.Version)
			if st.LoggedIn && st.User != nil {
				u := st.User.Name
				if st.User.GitHubLogin != "" {
					u += " " + sAccent.Render("@"+st.User.GitHubLogin)
				}
				e.o.kv("usuario", u)
			} else {
				e.o.kv("usuario", sWarn.Render("sin sesión · forge login"))
			}
			e.o.kv("api", st.APIURL)
			e.o.kv("org", st.Org)
			e.o.kv("activo desde", ago(st.StartedAt))
			pending := fmt.Sprint(st.Pending)
			if st.Pending > 0 {
				pending = sWarn.Render(pending)
			}
			e.o.kv("pendientes", pending)
			if st.LastSyncAt != nil {
				e.o.kv("última sync", ago(*st.LastSyncAt))
			}
			if st.LastSyncError != "" {
				e.o.kv("error sync", sErr.Render(st.LastSyncError))
			}
			for _, c := range st.Captures {
				last := sMuted.Render("sin eventos")
				if c.LastEventAt != nil {
					last = ago(*c.LastEventAt)
				}
				e.o.kv(string(c.Harness), fmt.Sprintf("%d hoy · %s", c.EventsToday, last))
			}
			if st.Override != nil {
				scope := "global"
				if st.Override.RepoPath != "" {
					scope = st.Override.RepoPath
				}
				e.o.kv("tarea forzada", sBold.Render(st.Override.Issue.String())+sMuted.Render(" ("+scope+")"))
			}
			return nil
		},
	}
}

func syncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Envía ya a la API los eventos pendientes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := load(cmd)
			if err != nil {
				return err
			}
			start := time.Now()
			r, err := e.daemon.Sync(cmd.Context())
			if err != nil {
				return err
			}
			e.o.ok("%d enviados · %d duplicados · %d rechazados · %d pendientes %s",
				r.Sent, r.Duplicates, r.Rejected, r.Pending, sMuted.Render(time.Since(start).Round(time.Millisecond).String()))
			return nil
		},
	}
}
