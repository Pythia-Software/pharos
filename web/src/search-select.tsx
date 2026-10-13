import React from "react";
import { Icon } from "./icons";

// Keep native selection and keyboard behavior, with the same inline icon as
// the rest of the editor. Data-URL background arrows are blocked by the CSP.
export function SearchSelect({ className = "", style, ...props }: React.SelectHTMLAttributes<HTMLSelectElement>) {
  const options: React.ReactElement<React.OptionHTMLAttributes<HTMLOptionElement>>[] = [];
  const collect = (children: React.ReactNode) => React.Children.forEach(children, child => {
    if (!React.isValidElement<{ children?: React.ReactNode }>(child)) return;
    if (child.type === "option") options.push(child);
    else collect(child.props.children);
  });
  collect(props.children);
  const selected = options.find(option => String(option.props.value) === String(props.value)) ?? options[0];
  return <span className={`text-select${className ? ` ${className}-wrap` : ""}`} style={style}>
    {/* Size to the selected label, rather than the longest hidden option. */}
    <span className="text-select-measure" aria-hidden="true">{selected?.props.label ?? selected?.props.children}</span>
    <select {...props} className={className || undefined} />
    <Icon name="chevron-down" />
  </span>;
}
