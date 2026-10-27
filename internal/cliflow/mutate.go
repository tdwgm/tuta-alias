package cliflow

import (
	"fmt"
	"io"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// Create adds a new alias to the account.
func Create(stdout, stderr io.Writer, mailAddress string) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	c := api.NewClient(consts.BaseURL, s.AccessToken)
	if err := services.AddAlias(c, s, mailAddress); err != nil {
		return bail(stderr, err)
	}
	fmt.Fprintf(stdout, "Created %s\n", mailAddress)
	return 0
}

// Delete disables an alias, or re-enables it when restore is true.
func Delete(stdout, stderr io.Writer, mailAddress string, restore bool) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	c := api.NewClient(consts.BaseURL, s.AccessToken)
	if err := services.RemoveAlias(c, s, mailAddress, restore); err != nil {
		return bail(stderr, err)
	}
	verb := "Removed"
	if restore {
		verb = "Restored"
	}
	fmt.Fprintf(stdout, "%s %s\n", verb, mailAddress)
	return 0
}
