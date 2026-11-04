package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"homeflix/internal/auth"
	"homeflix/internal/config"
	"homeflix/internal/store"
)

const userUsage = `Usage: homeflix user <subcommand> [flags] [name]

Subcommands:
  add <name>      create a user (-admin for an administrator)
  passwd <name>   set a new password; signs the user out everywhere
  admin <name>    make a user an administrator (-remove to take it back)
  delete <name>   delete a user with its sessions and preferences
  list            list users and their sessions

The password is read from the terminal without echo, or as the first line
of standard input when that is not a terminal.

Flags:
  -config path    configuration file (default ` + defaultConfigPath + `)
`

type cliIO struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

func userCommand(args []string, cio cliIO) error {
	if len(args) == 0 {
		fmt.Fprint(cio.stderr, userUsage)
		return errors.New("missing user subcommand")
	}
	sub := args[0]
	fs := flag.NewFlagSet("user "+sub, flag.ContinueOnError)
	fs.SetOutput(cio.stderr)
	fs.Usage = func() { fmt.Fprint(cio.stderr, userUsage) }
	configPath := fs.String("config", defaultConfigPath, "configuration file")
	admin, remove := false, false
	switch sub {
	case "add":
		fs.BoolVar(&admin, "admin", false, "make the user an administrator")
	case "admin":
		fs.BoolVar(&remove, "remove", false, "take administrator rights back")
	}
	names, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	wantNames := 1
	if sub == "list" {
		wantNames = 0
	}
	switch sub {
	case "add", "passwd", "admin", "delete", "list":
	default:
		fmt.Fprint(cio.stderr, userUsage)
		return fmt.Errorf("unknown user subcommand %q", sub)
	}
	if len(names) != wantNames {
		fmt.Fprint(cio.stderr, userUsage)
		return fmt.Errorf("user %s: want %d name argument(s), got %d", sub, wantNames, len(names))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	switch sub {
	case "add":
		name := strings.TrimSpace(names[0])
		if name == "" {
			return errors.New("user name must not be empty")
		}
		pw, err := readNewPassword(cio)
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		u, err := db.CreateUser(ctx, name, hash, admin)
		if err != nil {
			return err
		}
		role := "user"
		if u.IsAdmin {
			role = "administrator"
		}
		fmt.Fprintf(cio.stdout, "created %s %q (%s)\n", role, u.Name, u.ID)
	case "passwd":
		u, err := db.UserByName(ctx, names[0])
		if err != nil {
			return fmt.Errorf("user %q: %w", names[0], err)
		}
		pw, err := readNewPassword(cio)
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		if err := db.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		fmt.Fprintf(cio.stdout, "password changed for %q; all sessions signed out\n", u.Name)
	case "admin":
		u, err := db.UserByName(ctx, names[0])
		if err != nil {
			return fmt.Errorf("user %q: %w", names[0], err)
		}
		if err := db.SetAdmin(ctx, u.ID, !remove); err != nil {
			return err
		}
		if remove {
			fmt.Fprintf(cio.stdout, "%q is no administrator any more\n", u.Name)
		} else {
			fmt.Fprintf(cio.stdout, "%q is an administrator\n", u.Name)
		}
	case "delete":
		u, err := db.UserByName(ctx, names[0])
		if err != nil {
			return fmt.Errorf("user %q: %w", names[0], err)
		}
		if err := db.DeleteUser(ctx, u.ID); err != nil {
			return err
		}
		fmt.Fprintf(cio.stdout, "deleted user %q\n", u.Name)
	case "list":
		return listUsers(ctx, db, cio.stdout)
	}
	return nil
}

// parseInterspersed allows flags after positional arguments, so that
// "user add anna -admin" works like "user add -admin anna".
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func listUsers(ctx context.Context, db *store.Store, w io.Writer) error {
	users, err := db.Users(ctx)
	if err != nil {
		return err
	}
	tokens, err := db.Tokens(ctx, "")
	if err != nil {
		return err
	}
	byUser := map[string][]store.Token{}
	for _, t := range tokens {
		byUser[t.UserID] = append(byUser[t.UserID], t)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tROLE\tLAST LOGIN\tSESSIONS")
	for _, u := range users {
		role := "user"
		if u.IsAdmin {
			role = "admin"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", u.Name, role, formatTime(u.LastLoginAt), len(byUser[u.ID]))
		for _, t := range byUser[u.ID] {
			fmt.Fprintf(tw, "  %s %s on %s\t\tlast seen %s\tfrom %s\n",
				t.Client, t.ClientVersion, t.DeviceName, formatTime(t.LastSeenAt), t.RemoteAddr)
		}
	}
	return tw.Flush()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func readNewPassword(cio cliIO) (string, error) {
	if f, ok := cio.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cio.stderr, "Password: ")
		first, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cio.stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(cio.stderr, "Repeat password: ")
		second, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cio.stderr)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("passwords do not match")
		}
		return checkPassword(string(first))
	}
	line, err := bufio.NewReader(cio.stdin).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("no password on standard input")
	}
	return checkPassword(strings.TrimRight(line, "\r\n"))
}

func checkPassword(pw string) (string, error) {
	if pw == "" {
		return "", errors.New("password must not be empty")
	}
	return pw, nil
}
