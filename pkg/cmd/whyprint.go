package cmd

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	resourceapi "k8s.io/api/resource/v1"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
)

// printExplanation renders one explanation for people: a short header,
// then one section per topic, each only when it has something to say.
func printExplanation(w io.Writer, e *analyze.Explanation, st style) {
	field := func(key, value string) {
		fmt.Fprintf(w, "%s %s\n", st.bold(padRight(key+":", 9)), value)
	}
	field("Claim", e.Claim)
	if e.Pod != "" {
		field("Pod", e.Pod)
	}
	field("Verdict", verdictText(st, e.Verdict))
	field("Reason", e.Summary)

	if len(e.Allocation) > 0 {
		section(w, st, "Allocated devices")
		width := 0
		for _, r := range e.Allocation {
			width = max(width, len(r.Request))
		}
		for _, r := range e.Allocation {
			fmt.Fprintf(w, "  %s  %s/%s/%s\n", padRight(r.Request, width), r.Driver, r.Pool, r.Device)
		}
	}

	if e.PodScheduled != "" || len(e.Events) > 0 {
		section(w, st, "Scheduler")
		var rows [][2]string
		if e.PodScheduled != "" {
			rows = append(rows, [2]string{"PodScheduled", e.PodScheduled})
		}
		for _, ev := range e.Events {
			reason, message, _ := strings.Cut(ev, ": ")
			rows = append(rows, [2]string{reason, message})
		}
		width := 0
		for _, r := range rows {
			width = max(width, len(r[0]))
		}
		for _, r := range rows {
			fmt.Fprintf(w, "  %s  %s\n", st.dim(padRight(r[0], width)), r[1])
		}
	}

	if e.Pod != "" && (len(e.CandidateNodes) > 0 || len(e.ExcludedNodes) > 0) {
		total := len(e.CandidateNodes)
		for _, d := range e.ExcludedNodes {
			total += d.Count
		}
		if len(e.Allocation) > 0 {
			section(w, st, "Nodes")
			fmt.Fprintf(w, "  The claim's devices are usable on %s; the pod can run on %d of them.\n", nodes(total), len(e.CandidateNodes))
		} else {
			section(w, st, "Candidate nodes")
			fmt.Fprintf(w, "  The pod can run on %d of %s.\n", len(e.CandidateNodes), nodes(total))
		}
		for _, d := range e.ExcludedNodes {
			list := strings.Join(d.Examples, ", ")
			if more := d.Count - len(d.Examples); more > 0 {
				list += fmt.Sprintf(" +%d more", more)
			}
			fmt.Fprintf(w, "  %s %d %s: %s\n", st.red("✗"), d.Count, d.Reason, list)
		}
	}

	for _, r := range e.Requests {
		for i, f := range r.Alternatives {
			printFunnel(w, st, r, i, f)
		}
	}

	if len(e.PoolWarnings) > 0 {
		section(w, st, "Warnings")
		for _, p := range e.PoolWarnings {
			fmt.Fprintf(w, "  %s %s\n", st.yellow("!"), p)
		}
	}

	if a := e.Allocator; a != nil {
		section(w, st, "Allocator")
		switch {
		case len(a.Nodes) > 0:
			fmt.Fprintf(w, "  %s devices found on %d of %d candidate nodes: %s\n", st.green("✓"), len(a.Nodes), a.NodesTried, strings.Join(a.Nodes, ", "))
		case a.Error != "":
			fmt.Fprintf(w, "  %s rejects the claim on every candidate node: %s\n", st.red("✗"), a.Error)
		default:
			fmt.Fprintf(w, "  %s no allocation on any of the %d candidate nodes\n", st.red("✗"), a.NodesTried)
		}
		if len(a.AbortingNodes) > 0 {
			fmt.Fprintf(w, "  %s errors on %s, which abort scheduling if the scheduler evaluates them\n",
				st.yellow("!"), strings.Join(a.AbortingNodes, ", "))
		}
		for _, ne := range a.NodeErrors {
			fmt.Fprintf(w, "    %s\n", st.dim(ne))
		}
		features := strings.Join(a.Features, ", ")
		if features == "" {
			features = "none"
		}
		fmt.Fprintf(w, "  %s\n", st.dim("features assumed: "+features))
	}

	if len(e.Hints) > 0 {
		section(w, st, "Next steps")
		for _, h := range e.Hints {
			fmt.Fprintf(w, "  • %s\n", h)
		}
	}
}

// printFunnel renders one request (or firstAvailable alternative) as a
// table: each stage with the devices left, and the reasons devices were
// dropped on the same line, with examples underneath.
func printFunnel(w io.Writer, st style, r analyze.RequestExplanation, index int, f analyze.Funnel) {
	what := fmt.Sprintf("%d devices", f.Count)
	switch {
	case f.AllocationMode == string(resourceapi.DeviceAllocationModeAll):
		what = "all matching devices"
	case f.Count == 1:
		what = "1 device"
	}
	title := fmt.Sprintf("Request %q: %s from class %s", r.Name, what, f.Class)
	if len(r.Alternatives) > 1 {
		_, alt, _ := strings.Cut(f.Request, "/")
		title = fmt.Sprintf("Request %q, alternative %d of %d (%s): %s from class %s", r.Name, index+1, len(r.Alternatives), alt, what, f.Class)
	}
	if f.AdminAccess {
		title += ", admin access"
	}
	section(w, st, title)

	problem := strings.TrimPrefix(f.Problem, fmt.Sprintf("request %q: ", f.Request))
	switch {
	case f.Error != "":
		fmt.Fprintf(w, "  %s error: %s\n", st.red("✗"), f.Error)
		return
	case len(f.Stages) == 0:
		fmt.Fprintf(w, "  %s %s\n", st.red("✗"), problem)
		return
	}

	stageWidth, leftWidth, dropWidth := len("STAGE"), len("LEFT"), 0
	for _, s := range f.Stages {
		stageWidth = max(stageWidth, len(s.Name))
		leftWidth = max(leftWidth, len(strconv.Itoa(s.Remaining)))
		for _, d := range s.Dropped {
			dropWidth = max(dropWidth, len(strconv.Itoa(d.Count))+1)
		}
	}
	blank := "  " + strings.Repeat(" ", stageWidth) + "  " + strings.Repeat(" ", leftWidth) + "   "
	exampleIndent := blank + strings.Repeat(" ", dropWidth) + "   "

	fmt.Fprintf(w, "  %s\n", st.dim(padRight("STAGE", stageWidth)+"  "+padLeft("LEFT", leftWidth)+"   DROPPED"))
	for _, s := range f.Stages {
		left := padLeft(strconv.Itoa(s.Remaining), leftWidth)
		if s.Remaining == 0 {
			left = st.red(left)
		}
		lead := "  " + padRight(s.Name, stageWidth) + "  " + left + "   "
		if len(s.Dropped) == 0 {
			fmt.Fprintln(w, strings.TrimRight(lead, " "))
		}
		for i, d := range s.Dropped {
			if i > 0 {
				lead = blank
			}
			fmt.Fprintf(w, "%s%s %s\n", lead, st.red(padLeft("-"+strconv.Itoa(d.Count), dropWidth)), d.Reason)
			printExamples(w, st, exampleIndent, d)
		}
		if s.Remaining == 0 {
			break
		}
	}

	if f.Satisfiable {
		fmt.Fprintf(w, "  %s satisfiable on %s (best: %s, %d available, %d needed)\n",
			st.green("✓"), nodes(len(f.Nodes)), f.BestNode, f.BestCount, f.Need)
	} else {
		fmt.Fprintf(w, "  %s %s\n", st.red("✗"), problem)
	}
}

// printExamples lists example devices in two columns: the device, then
// what made it drop (attribute values, or the claim holding it).
func printExamples(w io.Writer, st style, indent string, d analyze.Drop) {
	type example struct{ id, detail string }
	var examples []example
	width := 0
	for _, ex := range d.Examples {
		id, detail := ex, ""
		if i := strings.Index(ex, " ("); i >= 0 && strings.HasSuffix(ex, ")") {
			id, detail = ex[:i], ex[i+2:len(ex)-1]
		}
		examples = append(examples, example{id, detail})
		width = max(width, len(id))
	}
	for _, ex := range examples {
		line := ex.id
		if ex.detail != "" {
			line = padRight(ex.id, width) + "  " + st.dim(ex.detail)
		}
		fmt.Fprintf(w, "%s%s\n", indent, line)
	}
	if more := d.Count - len(d.Examples); more > 0 {
		fmt.Fprintf(w, "%s%s\n", indent, st.dim(fmt.Sprintf("+%d more", more)))
	}
}

func section(w io.Writer, st style, title string) {
	fmt.Fprintf(w, "\n%s\n", st.bold(title))
}

func verdictText(st style, v analyze.Verdict) string {
	text := string(v)
	switch v {
	case analyze.VerdictAllocated, analyze.VerdictAllocatable:
		return st.bold(st.green(text))
	case analyze.VerdictAllocatedPodPending:
		return st.bold(st.yellow(text))
	default:
		return st.bold(st.red(text))
	}
}

func nodes(n int) string {
	if n == 1 {
		return "1 node"
	}
	return strconv.Itoa(n) + " nodes"
}
