package example

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"

	"github.com/Artui/go-services/aguix"
)

// bookID pulls the first number out of what the user typed.
var bookID = regexp.MustCompile(`\d+`)

// BorrowArgs builds a borrow_book call from the user's message.
//
// Returning an error ends the run, and the handler redacts it -- so its words
// are for the log. Telling the user what to type is the fallback rule's job.
func BorrowArgs(in aguix.RunInput) (json.RawMessage, error) {
	message, ok := in.LastUserMessage()
	if !ok {
		return nil, errors.New("the run carries no user message")
	}
	found := bookID.FindString(message.Content)
	if found == "" {
		return nil, errors.New("the message names no book")
	}
	id, err := strconv.ParseInt(found, 10, 64)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`{"book_id":` + strconv.FormatInt(id, 10) + `}`), nil
}

// Librarian is the scripted agent this module demonstrates and tests.
//
// It lives here rather than in the demo command so that the test and the
// browser drive the same script. A demo whose behaviour is only asserted by
// somebody opening it is a demo that quietly stops working.
//
// There is no model and no API key. What is scripted is which tool gets called;
// everything under that is the kernel every other adapter runs, against the
// same database.
func Librarian(toolbox aguix.Caller) aguix.Agent {
	return aguix.Scripted(
		aguix.Rule{
			When: aguix.WhenUserSays("borrow"),
			Steps: []aguix.Step{
				aguix.Say("Let me check the shelf."),
				aguix.CallToolFrom(toolbox, "borrow_book", BorrowArgs),
				// No closing line, and the absence is the point. A script has
				// no branch: it cannot read the result of the call it just
				// made, so any sentence here is asserted before the answer
				// exists. The first version of this said "That is done." and
				// said it over a refusal -- the tool result read "no copy is on
				// the shelf" and the assistant congratulated itself directly
				// underneath.
			},
		},
		aguix.Rule{
			When: aguix.WhenUserSays("books"),
			Steps: []aguix.Step{
				aguix.Say("Here is the catalogue."),
				aguix.CallTool(toolbox, "list_books", json.RawMessage(`{}`)),
			},
		},
		aguix.Rule{
			Steps: []aguix.Step{aguix.Say(
				"I can list the books, or borrow one for you. " +
					"Try \"show me the books\" or \"borrow book 10\".")},
		},
	)
}
