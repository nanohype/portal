import {
  useState,
  useRef,
  useEffect,
  useCallback,
  type ReactNode,
} from "react";
import { cn } from "@/lib/utils";
import { ChevronDown } from "lucide-react";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

interface SelectProps {
  value?: string;
  onChange?: (e: { target: { value: string } }) => void;
  children?: ReactNode;
  className?: string;
  placeholder?: string;
  disabled?: boolean;
}

/**
 * Custom accessible select component.
 *
 * Accepts either:
 *   - `<option>` children (parsed automatically) — drop-in replacement for native select
 *   - explicit options via the options prop (future)
 *
 * The onChange signature matches native select: `(e: { target: { value } }) => void`
 */
export function Select({
  value,
  onChange,
  children,
  className,
  placeholder,
  disabled,
}: SelectProps) {
  const [open, setOpen] = useState(false);
  const [highlightIndex, setHighlightIndex] = useState(-1);
  const containerRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Parse <option> children into structured options
  const options = parseOptions(children);

  const selected = options.find((o) => o.value === value);
  const displayLabel =
    selected?.label || placeholder || options[0]?.label || "";

  const select = useCallback(
    (val: string) => {
      onChange?.({ target: { value: val } });
      setOpen(false);
      setHighlightIndex(-1);
    },
    [onChange]
  );

  // Close on outside click
  useEffect(() => {
    if (!open) return;
    const handleClick = (e: MouseEvent) => {
      if (
        containerRef.current &&
        !containerRef.current.contains(e.target as Node)
      ) {
        setOpen(false);
        setHighlightIndex(-1);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [open]);

  // Keyboard navigation
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (disabled) return;

      if (!open) {
        if (
          e.key === "Enter" ||
          e.key === " " ||
          e.key === "ArrowDown" ||
          e.key === "ArrowUp"
        ) {
          e.preventDefault();
          setOpen(true);
          const idx = options.findIndex((o) => o.value === value);
          setHighlightIndex(idx >= 0 ? idx : 0);
        }
        return;
      }

      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          setHighlightIndex((prev) => {
            let next = prev + 1;
            while (next < options.length && options[next].disabled) next++;
            return next < options.length ? next : prev;
          });
          break;
        case "ArrowUp":
          e.preventDefault();
          setHighlightIndex((prev) => {
            let next = prev - 1;
            while (next >= 0 && options[next].disabled) next--;
            return next >= 0 ? next : prev;
          });
          break;
        case "Enter":
        case " ":
          e.preventDefault();
          if (
            highlightIndex >= 0 &&
            !options[highlightIndex].disabled
          ) {
            select(options[highlightIndex].value);
          }
          break;
        case "Escape":
          e.preventDefault();
          setOpen(false);
          setHighlightIndex(-1);
          break;
      }
    },
    [open, disabled, options, value, highlightIndex, select]
  );

  // Scroll highlighted item into view
  useEffect(() => {
    if (!open || highlightIndex < 0 || !listRef.current) return;
    const items = listRef.current.children;
    if (items[highlightIndex]) {
      (items[highlightIndex] as HTMLElement).scrollIntoView({
        block: "nearest",
      });
    }
  }, [highlightIndex, open]);

  return (
    <div ref={containerRef} className={cn("relative", className)}>
      <button
        type="button"
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        disabled={disabled}
        onClick={() => {
          if (!disabled) {
            setOpen(!open);
            if (!open) {
              const idx = options.findIndex((o) => o.value === value);
              setHighlightIndex(idx >= 0 ? idx : 0);
            }
          }
        }}
        onKeyDown={handleKeyDown}
        className={cn(
          "flex h-8 w-full items-center justify-between rounded-[6px] border border-input-border bg-input px-2.5 text-sm transition-all duration-150",
          "hover:border-primary/30 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring/30",
          "disabled:cursor-not-allowed disabled:opacity-40",
          open && "ring-1 ring-ring/30",
        )}
      >
        <span
          className={cn(
            !selected || (selected.value === "" && placeholder)
              ? "text-dim"
              : ""
          )}
        >
          {displayLabel}
        </span>
        <ChevronDown
          className={cn(
            "w-3.5 h-3.5 text-muted-foreground/50 shrink-0 ml-2 transition-transform duration-150",
            open && "rotate-180"
          )}
        />
      </button>

      {open && (
        <div
          ref={listRef}
          role="listbox"
          className="absolute z-50 mt-1 w-full min-w-[8rem] max-h-60 overflow-auto rounded-[6px] border border-border bg-card/80 backdrop-blur-xl shadow-xl shadow-black/30 py-1 animate-fade-in"
          style={{ animationDuration: "120ms" }}
        >
          {options.map((option, i) => {
            const isSelected = option.value === value;
            const isHighlighted = i === highlightIndex;
            return (
              <div
                key={`${option.value}-${i}`}
                role="option"
                aria-selected={isSelected}
                aria-disabled={option.disabled}
                onClick={() => {
                  if (!option.disabled) select(option.value);
                }}
                onMouseEnter={() => {
                  if (!option.disabled) setHighlightIndex(i);
                }}
                className={cn(
                  "px-2.5 py-1.5 text-sm cursor-pointer transition-colors duration-75",
                  option.disabled && "opacity-40 cursor-not-allowed",
                  isHighlighted && !option.disabled && "bg-hover",
                  isSelected && "text-primary border-l-2 border-primary pl-2",
                  !isSelected && "border-l-2 border-transparent",
                )}
              >
                <span>{option.label}</span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

/** Parse <option> children into SelectOption[] */
function parseOptions(children: ReactNode): SelectOption[] {
  const opts: SelectOption[] = [];
  const items = Array.isArray(children) ? children : [children];

  for (const child of items.flat(Infinity)) {
    if (!child || typeof child !== "object" || !("props" in child)) continue;
    const props = child.props as Record<string, unknown>;
    if (child.type === "option") {
      const text = optionChildText(props.children);
      opts.push({
        value: String(props.value ?? text),
        label: text || String(props.value ?? ""),
        disabled: Boolean(props.disabled),
      });
    }
  }
  return opts;
}

/**
 * Flatten an <option>'s children into a label string, joining with "" — NOT
 * String(children), which coerces a multi-child array (e.g. `{a.name} ({a.id})`
 * → ["prod"," (","111111111111",")"]) via Array.toString and splices commas
 * into the label ("prod, (,111111111111,)").
 */
function optionChildText(children: unknown): string {
  if (children == null || typeof children === "boolean") return "";
  if (Array.isArray(children)) return children.map(optionChildText).join("");
  if (typeof children === "object" && "props" in children) {
    return optionChildText(
      (children as { props?: { children?: unknown } }).props?.children
    );
  }
  return String(children);
}
