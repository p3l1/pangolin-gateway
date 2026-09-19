#!/bin/sh
# Splices controller-gen's RBAC rules into the chart's ClusterRole, keeping the
# hand-written leases rule leader election needs alongside them.
#
# controller-gen only promotes a +kubebuilder:rbac comment block to package level
# when it is separated from the next declaration by a blank line; placed directly
# above a func with no blank line, it becomes that func's godoc and is discarded.
set -eu

generated=$1
chart_rbac=$2

{
	sed -n '1,/^  # BEGIN GENERATED/p' "$chart_rbac"
	echo '  - apiGroups: [coordination.k8s.io]'
	echo '    resources: [leases]'
	echo '    verbs: [get, list, watch, create, update, patch, delete]'
	if [ -f "$generated" ]; then
		# controller-gen writes rule items flush with "rules:" at zero indentation;
		# the chart nests them one level under its own "rules:" key, so they are
		# re-indented to match rather than spliced in verbatim.
		awk '/^rules:/ { f = 1; next } f' "$generated" | sed 's/^/  /'
	fi
	sed -n '/^  # END GENERATED/,$p' "$chart_rbac"
} > "$chart_rbac.tmp"
mv "$chart_rbac.tmp" "$chart_rbac"
