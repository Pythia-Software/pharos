import React from "react";
import { Icon } from "./icons";

// An invisible copy of the selected label sizes the native control, including
// in desktop webviews without field-sizing support. The menu keeps every option.
export function DashboardSelect(props: Omit<React.SelectHTMLAttributes<HTMLSelectElement>, "multiple" | "size">) {
  const options: React.ReactElement<React.OptionHTMLAttributes<HTMLOptionElement>>[] = [];
  const collect = (children: React.ReactNode) => React.Children.forEach(children, child => {
    if (!React.isValidElement<{ children?: React.ReactNode }>(child)) return;
    if (child.type === "option") options.push(child);
    else collect(child.props.children);
  });
  collect(props.children);
  const selected = options.find(option => String(option.props.value ?? option.props.children) === String(props.value)) ?? options[0];
  return <span className="dashboard-select">
    <span className="dashboard-select-measure" aria-hidden="true">{selected?.props.label ?? selected?.props.children}</span>
    <select {...props} />
    <Icon name="chevron-down" className="dashboard-select-caret" />
  </span>;
}
