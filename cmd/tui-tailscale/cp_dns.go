package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-tailscale/internal/headscale"
)

// This file is the dns screen (issue #14): headscale's `dns:` section, one row
// per setting, split domain and extra record, each edited with the same
// minimal diff of config.yaml and the same restart at the end that S and O
// use. `e` edits the selected row, `n` adds a split domain or a record, `x`
// removes the selected one.

// dnsRowKind is what a row of the dns screen stands for.
type dnsRowKind int

const (
	dnsRowMagic dnsRowKind = iota
	dnsRowBase
	dnsRowOverride
	dnsRowGlobal
	dnsRowSplit
	dnsRowSplitNone
	dnsRowSearch
	dnsRowRecord
	dnsRowRecordNone
	dnsRowRecordsPath
)

// dnsRow is one row: its kind, the index of the split entry or record it
// stands for, and its two cells.
type dnsRow struct {
	kind         dnsRowKind
	index        int
	label, value string
}

// dnsDraft is the section as the open dns step would leave it.
type dnsDraft struct {
	want headscale.DNSConfig
	// index is the split entry or record being edited, -1 for a new one;
	// domain is a new split entry's domain, between its two steps.
	index  int
	domain string
}

// The dns screen's pickers' options.
const (
	dnsNewSplit  = "split DNS — a domain answered by its own nameservers"
	dnsNewRecord = "extra record — a name headscale answers itself (A or AAAA)"
)

// dnsRows builds the rows from the section as config.yaml has it.
func (a *app) dnsRows() []dnsRow {
	cp := a.hsState.ControlPlane
	if !a.hsState.Present || !cp.Readable {
		return nil
	}
	d := cp.DNS
	override := "yes — nodes use these settings instead of their own"
	if !d.OverrideLocalDNS {
		override = "no — nodes keep their own resolvers and add these"
	}
	rows := []dnsRow{
		{kind: dnsRowMagic, label: "magic_dns", value: onOff(d.MagicDNS)},
		{kind: dnsRowBase, label: "base_domain", value: orDash(d.BaseDomain)},
		{kind: dnsRowOverride, label: "override_local_dns", value: override},
		{kind: dnsRowGlobal, label: "nameservers.global", value: listOrDash(d.Global)},
	}
	if len(d.Split) == 0 {
		rows = append(rows, dnsRow{kind: dnsRowSplitNone, label: "nameservers.split",
			value: "none · n adds a domain"})
	}
	for i, s := range d.Split {
		rows = append(rows, dnsRow{kind: dnsRowSplit, index: i, label: "split " + s.Domain,
			value: strings.Join(s.Nameservers, ", ")})
	}
	rows = append(rows, dnsRow{kind: dnsRowSearch, label: "search_domains",
		value: listOrDash(d.SearchDomains)})
	switch {
	case d.ExtraRecordsPath != "":
		rows = append(rows, dnsRow{kind: dnsRowRecordsPath, label: "extra_records_path",
			value: d.ExtraRecordsPath + " — records come from that file"})
	case len(d.ExtraRecords) == 0:
		rows = append(rows, dnsRow{kind: dnsRowRecordNone, label: "extra_records",
			value: "none · n adds one"})
	}
	for i, r := range d.ExtraRecords {
		rows = append(rows, dnsRow{kind: dnsRowRecord, index: i, label: "record " + r.Name,
			value: r.Type + " " + r.Value})
	}
	return rows
}

// dnsTable renders the rows.
func (a *app) dnsTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "SETTING", Width: 38},
		{Title: "VALUE", Width: 30, Flex: true},
	}
	rows := a.dnsRows()
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{r.label, r.value})
	}
	return columns, out, nil
}

// selectedDNSRow is the highlighted row.
func (a *app) selectedDNSRow() (dnsRow, bool) {
	rows := a.dnsRows()
	i := a.cursor[screenDNS]
	if i < 0 || i >= len(rows) {
		return dnsRow{}, false
	}
	return rows[i], true
}

// handleDNSKey dispatches the dns screen's keys.
func (a *app) handleDNSKey(key string) tea.Cmd {
	if key != "e" && key != "n" && key != "x" {
		return nil
	}
	if !a.controlPlaneEditable() {
		return nil
	}
	a.dnsDraft = dnsDraft{want: cloneDNS(a.hsState.ControlPlane.DNS), index: -1}
	row, ok := a.selectedDNSRow()
	switch key {
	case "n":
		a.picker = ui.NewPicker("DNS — add what?", []string{dnsNewSplit, dnsNewRecord}, dnsNewSplit)
		a.pickerPurpose = pickerDNSNew
		a.mode = modePicker
		return nil
	case "x":
		return a.deleteDNSRow(row, ok)
	}
	if !ok {
		return a.warnNothing()
	}
	return a.editDNSRow(row)
}

// editDNSRow opens the step that edits one row.
func (a *app) editDNSRow(row dnsRow) tea.Cmd {
	d := a.dnsDraft.want
	switch row.kind {
	case dnsRowMagic:
		a.openPicker(pickerDNSMagic, "DNS — MagicDNS: give every node a name under base_domain?",
			d.MagicDNS)
	case dnsRowBase:
		a.askDNSBase(d.BaseDomain, nil)
	case dnsRowOverride:
		a.openPicker(pickerDNSOverride, "DNS — override_local_dns: nodes use these nameservers "+
			"instead of their own?", d.OverrideLocalDNS)
	case dnsRowGlobal:
		a.askDNSGlobal(strings.Join(d.Global, ", "), nil)
	case dnsRowSearch:
		a.askDNSSearch(strings.Join(d.SearchDomains, ", "), nil)
	case dnsRowSplit:
		a.dnsDraft.index = row.index
		a.dnsDraft.domain = d.Split[row.index].Domain
		a.askDNSSplitServers(strings.Join(d.Split[row.index].Nameservers, ", "), nil)
	case dnsRowSplitNone:
		a.askDNSSplitDomain("", nil)
	case dnsRowRecord:
		a.dnsDraft.index = row.index
		a.askDNSRecord(d.ExtraRecords[row.index].String(), nil)
	case dnsRowRecordNone:
		a.askDNSRecord("", nil)
	case dnsRowRecordsPath:
		a.setStatus(ui.StatusInfo, "the records come from "+d.ExtraRecordsPath+
			": headscale reloads that file on change; edit it there")
	}
	return nil
}

// deleteDNSRow removes the selected split domain or record.
func (a *app) deleteDNSRow(row dnsRow, ok bool) tea.Cmd {
	d := &a.dnsDraft.want
	switch {
	case ok && row.kind == dnsRowSplit:
		removed := d.Split[row.index].Domain
		d.Split = slices.Delete(d.Split, row.index, row.index+1)
		return a.confirmDNS("DNS — stop answering " + removed + " through its own nameservers; " +
			"its names resolve through the global nameservers again.")
	case ok && row.kind == dnsRowRecord:
		removed := d.ExtraRecords[row.index]
		d.ExtraRecords = slices.Delete(d.ExtraRecords, row.index, row.index+1)
		return a.confirmDNS("DNS — remove the record " + removed.String() + ".")
	}
	a.setStatus(ui.StatusInfo, "x removes a split domain or an extra record: select one")
	return nil
}

// tookDNSNew opens the first step of what n adds.
func (a *app) tookDNSNew(choice string) tea.Cmd {
	if choice == dnsNewRecord {
		if path := a.dnsDraft.want.ExtraRecordsPath; path != "" {
			a.setStatus(ui.StatusWarn, "extra_records_path is set: headscale reads the records "+
				"from "+path+", so add them there")
			return nil
		}
		a.askDNSRecord("", nil)
		return nil
	}
	a.askDNSSplitDomain("", nil)
	return nil
}

// tookDNSMagic records the MagicDNS switch; turning it on without a base
// domain asks for one, because headscale refuses to start that way.
func (a *app) tookDNSMagic(on bool) tea.Cmd {
	a.dnsDraft.want.MagicDNS = on
	if on && a.dnsDraft.want.BaseDomain == "" {
		a.askDNSBase("", nil)
		return nil
	}
	intro := "DNS — MagicDNS " + onOff(on) + "."
	if !on {
		intro += " Nodes stop getting names under base_domain; they are reached by address."
	}
	return a.confirmDNS(intro)
}

// tookDNSOverride records the override switch.
func (a *app) tookDNSOverride(on bool) tea.Cmd {
	a.dnsDraft.want.OverrideLocalDNS = on
	intro := "DNS — override_local_dns " + yesNo(on) + ": nodes use headscale's nameservers " +
		"instead of their own."
	if !on {
		intro = "DNS — override_local_dns no: nodes keep their own resolvers for everything " +
			"else, and use headscale's only for MagicDNS names and the split domains."
	}
	return a.confirmDNS(intro)
}

// askDNSBase opens the base domain step.
func (a *app) askDNSBase(value string, problem error) {
	help := "The MagicDNS domain: every node gets a name under it (laptop.<base_domain>). " +
		"It must not contain the server_url host (" +
		orDash(headscale.URLHost(a.hsState.ControlPlane.ServerURL)) + "): headscale " +
		"refuses to start that way."
	if !a.dnsDraft.want.MagicDNS {
		help += " MagicDNS is off, so it may be left empty."
	}
	a.openRetry(inputDNSBase, "DNS — base_domain", "tailnet.internal", value, help, problem)
}

// tookDNSBase checks the base domain against the whole section.
func (a *app) tookDNSBase(value string) tea.Cmd {
	a.dnsDraft.want.BaseDomain = strings.ToLower(strings.TrimSuffix(value, "."))
	if err := a.dnsDraft.want.Check(a.hsState.ControlPlane.ServerURL); err != nil {
		a.askDNSBase(value, err)
		return nil
	}
	return a.confirmDNS("DNS — base_domain " + orDash(a.dnsDraft.want.BaseDomain) +
		": node names move under it.")
}

// askDNSGlobal opens the global nameservers step.
func (a *app) askDNSGlobal(value string, problem error) {
	a.openRetry(inputDNSGlobal, "DNS — global nameservers", "1.1.1.1, 1.0.0.1", value,
		"The resolvers every node uses for names outside the tailnet, comma-separated: IP "+
			"addresses, or an https:// DNS-over-HTTPS URL. Empty leaves resolving to each "+
			"node's own settings.", problem)
}

// tookDNSGlobal checks and records the global nameservers.
func (a *app) tookDNSGlobal(value string) tea.Cmd {
	list := headscale.SplitList(value)
	for _, ns := range list {
		if p := headscale.NameserverProblem(ns); p != "" {
			a.askDNSGlobal(value, errors.New(p))
			return nil
		}
	}
	a.dnsDraft.want.Global = list
	return a.confirmDNS("DNS — global nameservers " + listOrDash(list) + ".")
}

// askDNSSearch opens the search domains step.
func (a *app) askDNSSearch(value string, problem error) {
	a.openRetry(inputDNSSearch, "DNS — search domains", "tailnet.internal", value,
		"Domains a bare name is tried under, comma-separated (ssh app-1 → app-1.<domain>). "+
			"With MagicDNS on, base_domain is always searched first.", problem)
}

// tookDNSSearch checks and records the search domains.
func (a *app) tookDNSSearch(value string) tea.Cmd {
	list := headscale.SplitList(value)
	for _, domain := range list {
		if p := headscale.DomainProblem(domain); p != "" {
			a.askDNSSearch(value, errors.New(p))
			return nil
		}
	}
	a.dnsDraft.want.SearchDomains = list
	return a.confirmDNS("DNS — search domains " + listOrDash(list) + ".")
}

// askDNSSplitDomain opens a new split entry's first step.
func (a *app) askDNSSplitDomain(value string, problem error) {
	a.openRetry(inputDNSSplitDomain, "Split DNS — domain", "corp.example.com", value,
		"Names under this domain are answered by the nameservers of the next step instead "+
			"of the global ones: a private network's own resolver, reached through the "+
			"tailnet (a cloud VCN's 169.254.169.254 for its internal domain, say).", problem)
}

// tookDNSSplitDomain checks the new domain and asks for its nameservers.
func (a *app) tookDNSSplitDomain(value string) tea.Cmd {
	domain := strings.ToLower(strings.TrimSuffix(value, "."))
	if p := headscale.DomainProblem(domain); p != "" {
		a.askDNSSplitDomain(value, errors.New(p))
		return nil
	}
	for _, s := range a.dnsDraft.want.Split {
		if s.Domain == domain {
			a.askDNSSplitDomain(value, fmt.Errorf("%s already has nameservers: e edits them", domain))
			return nil
		}
	}
	a.dnsDraft.domain, a.dnsDraft.index = domain, -1
	a.askDNSSplitServers("", nil)
	return nil
}

// askDNSSplitServers opens a split entry's nameservers step.
func (a *app) askDNSSplitServers(value string, problem error) {
	help := "The nameservers that answer for " + a.dnsDraft.domain + ", comma-separated IP " +
		"addresses."
	if a.dnsDraft.index >= 0 {
		help += " Empty removes the domain."
	}
	a.openRetry(inputDNSSplitServers, "Split DNS — nameservers for "+a.dnsDraft.domain,
		"10.0.0.2", value, help, problem)
}

// tookDNSSplitServers checks the nameservers and opens the confirm.
func (a *app) tookDNSSplitServers(value string) tea.Cmd {
	list := headscale.SplitList(value)
	d := &a.dnsDraft
	if len(list) == 0 {
		if d.index < 0 {
			a.askDNSSplitServers(value, errors.New("a split domain needs at least one nameserver"))
			return nil
		}
		d.want.Split = slices.Delete(d.want.Split, d.index, d.index+1)
		return a.confirmDNS("DNS — stop answering " + d.domain + " through its own nameservers.")
	}
	for _, ns := range list {
		if p := headscale.NameserverProblem(ns); p != "" {
			a.askDNSSplitServers(value, errors.New(p))
			return nil
		}
	}
	entry := headscale.SplitDNS{Domain: d.domain, Nameservers: list}
	if d.index >= 0 {
		d.want.Split[d.index] = entry
	} else {
		d.want.Split = append(d.want.Split, entry)
	}
	return a.confirmDNS("DNS — names under " + d.domain + " are answered by " +
		strings.Join(list, ", ") + ".")
}

// askDNSRecord opens the record step.
func (a *app) askDNSRecord(value string, problem error) {
	base := a.dnsDraft.want.BaseDomain
	placeholder := "app-1.tailnet.example.com A 100.64.0.10"
	if base != "" {
		placeholder = "app-1." + base + " A 100.64.0.10"
	}
	help := "One record: name, type and address, separated by spaces. A (IPv4) and AAAA " +
		"(IPv6) are what a Tailscale client resolves; a name under base_domain (" +
		orDash(base) + ") reads as part of the tailnet."
	if a.dnsDraft.index >= 0 {
		help += " Empty removes the record."
	}
	a.openRetry(inputDNSRecord, "DNS — extra record", placeholder, value, help, problem)
}

// tookDNSRecord checks the record and opens the confirm.
func (a *app) tookDNSRecord(value string) tea.Cmd {
	d := &a.dnsDraft
	if strings.TrimSpace(value) == "" {
		if d.index < 0 {
			a.cancelled()
			return nil
		}
		removed := d.want.ExtraRecords[d.index]
		d.want.ExtraRecords = slices.Delete(d.want.ExtraRecords, d.index, d.index+1)
		return a.confirmDNS("DNS — remove the record " + removed.String() + ".")
	}
	record, err := headscale.ParseRecord(value)
	if err != nil {
		a.askDNSRecord(value, err)
		return nil
	}
	for i, r := range d.want.ExtraRecords {
		if i != d.index && r.Name == record.Name && r.Type == record.Type {
			a.askDNSRecord(value, fmt.Errorf("%s already has an %s record: e edits it",
				record.Name, record.Type))
			return nil
		}
	}
	if d.index >= 0 {
		d.want.ExtraRecords[d.index] = record
	} else {
		d.want.ExtraRecords = append(d.want.ExtraRecords, record)
	}
	intro := "DNS — " + record.Name + " resolves to " + record.Value + " on every node."
	if headscale.RecordOutsideBase(record, d.want.BaseDomain) {
		intro += "\nNOTE: the name is not under base_domain (" + d.want.BaseDomain + "); only " +
			"nodes resolving through the tailnet see it, and it may shadow a public name."
	}
	return a.confirmDNS(intro)
}

// confirmDNS proves the change and opens the diff of config.yaml, with the
// restart chained behind it.
func (a *app) confirmDNS(intro string) tea.Cmd {
	cp := a.hsState.ControlPlane
	edits, err := headscale.PlanDNSChange(cp.Raw, cp.ServerURL, cp.DNS, a.dnsDraft.want)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	if len(edits) == 0 {
		a.setStatus(ui.StatusInfo, cp.ConfigPath+" already says this — nothing to write")
		return nil
	}
	return a.confirmConfigWrite(intro+"\n\n"+fmt.Sprintf("Step 1 of %d — rewrite ",
		1+headscale.TailSteps(cp))+headscale.HeadscaleConfigPath+
		". Only the lines below change; lists and maps are written on one line.", edits)
}

// cloneDNS copies a section, so a draft never writes through to the state.
func cloneDNS(d headscale.DNSConfig) headscale.DNSConfig {
	d.Global = slices.Clone(d.Global)
	d.SearchDomains = slices.Clone(d.SearchDomains)
	d.ExtraRecords = slices.Clone(d.ExtraRecords)
	split := make([]headscale.SplitDNS, 0, len(d.Split))
	for _, s := range d.Split {
		split = append(split, headscale.SplitDNS{Domain: s.Domain,
			Nameservers: slices.Clone(s.Nameservers)})
	}
	d.Split = split
	return d
}

// handleDNSInput routes a finished dns input to its step.
func (a *app) handleDNSInput(purpose inputPurpose, value string) (tea.Cmd, bool) {
	switch purpose {
	case inputDNSBase:
		return a.tookDNSBase(value), true
	case inputDNSGlobal:
		return a.tookDNSGlobal(value), true
	case inputDNSSearch:
		return a.tookDNSSearch(value), true
	case inputDNSSplitDomain:
		return a.tookDNSSplitDomain(value), true
	case inputDNSSplitServers:
		return a.tookDNSSplitServers(value), true
	case inputDNSRecord:
		return a.tookDNSRecord(value), true
	}
	return nil, false
}
