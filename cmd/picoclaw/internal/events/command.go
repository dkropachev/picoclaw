package events

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
)

// NewEventsCommand returns the live durable-event operator command.
func NewEventsCommand() *cobra.Command {
	return newEventsCommand(newGatewayClient())
}

func newEventsCommand(client *gatewayClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "events",
		Aliases: []string{"event"},
		Short:   "Inspect and replay durable external events",
		Long: "Inspect the durable external-event inbox through the currently running gateway.\n\n" +
			"These commands require a live gateway with event ingress enabled and never open the event database directly.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newListCommand(client),
		newGetCommand(client),
		newPayloadCommand(client),
		newDispatchesCommand(client),
		newReplayCommand(client),
	)
	return cmd
}

func newListCommand(client *gatewayClient) *cobra.Command {
	var (
		source        string
		connector     string
		eventType     string
		routingStatus string
		limit         int
		cursor        string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List durable external events",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := url.Values{}
			addQueryValue(query, "source", source)
			addQueryValue(query, "connector", connector)
			addQueryValue(query, "type", eventType)
			addQueryValue(query, "routing_status", routingStatus)
			addQueryValue(query, "cursor", cursor)
			if limit != 0 {
				query.Set("limit", strconv.Itoa(limit))
			}
			response, err := client.get(
				commandContext(cmd),
				eventoperator.RoutePrefix+"events",
				query,
			)
			if err != nil {
				return err
			}
			return printJSON(cmd, response)
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "Filter by exact event source")
	cmd.Flags().StringVar(&connector, "connector", "", "Filter by exact connector")
	cmd.Flags().StringVar(&eventType, "type", "", "Filter by exact event type")
	cmd.Flags().StringVar(
		&routingStatus,
		"routing-status",
		"",
		"Filter by routing status: pending, claimed, succeeded, or dead",
	)
	cmd.Flags().IntVar(
		&limit,
		"limit",
		0,
		fmt.Sprintf(
			"Page size (gateway default %d, maximum %d)",
			eventoperator.DefaultLimit,
			eventoperator.MaximumLimit,
		),
	)
	cmd.Flags().StringVar(&cursor, "cursor", "", "Opaque cursor returned by the previous page")
	return cmd
}

func newGetCommand(client *gatewayClient) *cobra.Command {
	return &cobra.Command{
		Use:   "get EVENT_ID",
		Short: "Show one durable external event without its payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validEventID(args[0]) {
				return errorsInvalidEventID()
			}
			response, err := client.get(
				commandContext(cmd),
				eventoperator.RoutePrefix+"events/"+args[0],
				nil,
			)
			if err != nil {
				return err
			}
			return printJSON(cmd, response)
		},
	}
}

func newPayloadCommand(client *gatewayClient) *cobra.Command {
	return &cobra.Command{
		Use:   "payload EVENT_ID",
		Short: "Print one event's exact redacted JSON payload",
		Long: "Print the already-redacted payload exactly as stored by the live gateway.\n\n" +
			"The bounded JSON object is written without parsing, reformatting, or adding a newline.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validEventID(args[0]) {
				return errorsInvalidEventID()
			}
			response, err := client.payload(commandContext(cmd), args[0])
			if err != nil {
				return err
			}
			return printJSON(cmd, response)
		},
	}
}

func newDispatchesCommand(client *gatewayClient) *cobra.Command {
	var (
		eventID     string
		workflowRef string
		status      string
		limit       int
		cursor      string
	)
	cmd := &cobra.Command{
		Use:   "dispatches",
		Short: "List durable event workflow dispatches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if eventID != "" && !validEventID(eventID) {
				return errorsInvalidEventID()
			}
			query := url.Values{}
			addQueryValue(query, "event_id", eventID)
			addQueryValue(query, "workflow_ref", workflowRef)
			addQueryValue(query, "status", status)
			addQueryValue(query, "cursor", cursor)
			if limit != 0 {
				query.Set("limit", strconv.Itoa(limit))
			}
			response, err := client.get(
				commandContext(cmd),
				eventoperator.RoutePrefix+"dispatches",
				query,
			)
			if err != nil {
				return err
			}
			return printJSON(cmd, response)
		},
	}
	cmd.Flags().StringVar(&eventID, "event-id", "", "Filter by exact event ID")
	cmd.Flags().StringVar(&workflowRef, "workflow", "", "Filter by exact workflow reference")
	cmd.Flags().StringVar(
		&status,
		"status",
		"",
		"Filter by status: pending, claimed, running, succeeded, failed, or dead",
	)
	cmd.Flags().IntVar(
		&limit,
		"limit",
		0,
		fmt.Sprintf(
			"Page size (gateway default %d, maximum %d)",
			eventoperator.DefaultLimit,
			eventoperator.MaximumLimit,
		),
	)
	cmd.Flags().StringVar(&cursor, "cursor", "", "Opaque cursor returned by the previous page")
	return cmd
}

func newReplayCommand(client *gatewayClient) *cobra.Command {
	var confirmed bool
	cmd := &cobra.Command{
		Use:   "replay EVENT_ID --yes",
		Short: "Create one additive replay of a durable event",
		Long: "Create one additive replay through the live gateway.\n\n" +
			"Replay creates a new event and may repeat workflow or external side effects. " +
			"The request is sent exactly once and requires --yes.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirmed {
				return fmt.Errorf("replay requires --yes because effects may repeat")
			}
			if !validEventID(args[0]) {
				return errorsInvalidEventID()
			}
			response, err := client.replay(commandContext(cmd), args[0])
			if err != nil {
				return err
			}
			return printJSON(cmd, response)
		},
	}
	cmd.Flags().BoolVar(&confirmed, "yes", false, "Confirm creation of a new event and possible repeated effects")
	return cmd
}

func addQueryValue(values url.Values, name, value string) {
	if value != "" {
		values.Set(name, value)
	}
}

func validEventID(id string) bool {
	const prefix = "ev_"
	if len(id) != len(prefix)+32 || id[:len(prefix)] != prefix {
		return false
	}
	for _, char := range id[len(prefix):] {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func errorsInvalidEventID() error {
	return fmt.Errorf("event ID must be ev_ followed by 32 lowercase hexadecimal characters")
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func printJSON(cmd *cobra.Command, response []byte) error {
	_, err := cmd.OutOrStdout().Write(response)
	return err
}
