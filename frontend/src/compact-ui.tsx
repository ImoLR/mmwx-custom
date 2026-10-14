import React, { useEffect, useRef } from "react";
import { ChevronDown, X } from "lucide-react";

export function CompactSheet({ title, subtitle, className = "", onClose, children, footer, header, bodyClassName = "", closeLabel = "关闭面板", dialogRef, onChangeCapture, closeDisabled = false, backdropClose = "click" }: { title: string; subtitle?: string; className?: string; onClose: () => void; children: React.ReactNode; footer?: React.ReactNode; header?: React.ReactNode; bodyClassName?: string; closeLabel?: string; dialogRef?: React.RefObject<HTMLElement | null>; onChangeCapture?: React.FormEventHandler<HTMLElement>; closeDisabled?: boolean; backdropClose?: "click" | "mousedown" }) {
  const localRef = useRef<HTMLElement>(null);
  const ref = dialogRef || localRef;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    ref.current?.focus();
    return () => { document.body.style.overflow = overflow; previous?.focus(); };
  }, []);
  return <div className="cx-dialog-layer cx-sheet-layer" onClick={backdropClose === "click" ? onClose : undefined} onMouseDown={backdropClose === "mousedown" ? onClose : undefined}>
    <section ref={ref} onChangeCapture={onChangeCapture} className={`cx-dialog cx-sheet ${className}`} role="dialog" aria-modal="true" aria-label={title} tabIndex={-1} onClick={(event) => event.stopPropagation()} onMouseDown={backdropClose === "mousedown" ? (event) => event.stopPropagation() : undefined} onKeyDown={(event) => {
      if ((event.target as HTMLElement).closest('[role="dialog"]') !== ref.current) return;
      if (event.key === "Escape") { event.stopPropagation(); onClose(); }
      if (event.key === "Tab") {
        const elements = [...ref.current!.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), summary, [tabindex="0"]')].filter((element) => element.getClientRects().length > 0);
        const first = elements[0], last = elements[elements.length - 1];
        if (event.shiftKey && (document.activeElement === first || document.activeElement === ref.current)) { event.preventDefault(); last?.focus(); }
        else if (!event.shiftKey && (document.activeElement === last || document.activeElement === ref.current)) { event.preventDefault(); first?.focus(); }
      }
    }}>
      <div className="cx-sheet-grab" />
      <header><div>{header || <h2>{title}</h2>}{subtitle && <p>{subtitle}</p>}</div><button type="button" disabled={closeDisabled} onClick={onClose} aria-label={closeLabel}><X /></button></header>
      <div className={`cx-dialog-body ${bodyClassName}`}>{children}</div>{footer && <footer className="cx-sheet-footer">{footer}</footer>}
    </section>
  </div>;
}

export function CompactSegments<T extends string>({ label, value, options, onChange, className = "" }: {
  label: string; value: T; options: ReadonlyArray<readonly [T, React.ReactNode]>; onChange: (value: T) => void; className?: string;
}) {
  return <div className={`cx-segments ${className}`} aria-label={label} style={{ gridTemplateColumns: `repeat(${options.length}, minmax(0, 1fr))` }}>{options.map(([key, text]) => <button type="button" key={key} aria-pressed={value === key} onClick={() => onChange(key)}>{text}</button>)}</div>;
}

export function CompactGroupHeader({ name, subtitle, count, closed, onToggle, avatar, children }: {
  name: string; subtitle?: string; count: React.ReactNode; closed: boolean; onToggle: () => void; avatar?: string; children?: React.ReactNode;
}) {
  return <div className="cx-group-head node-group-head">
    {children}
    <button type="button" className="cx-group-toggle node-group-toggle" aria-expanded={!closed} onClick={onToggle}>
      <span className={`cx-avatar tone-${name.charCodeAt(0) % 5}`}>{avatar || name.slice(0, 1)}</span>
      <span className="cx-group-label node-group-label"><strong>{name}</strong><small title={subtitle}>{subtitle}</small></span>
      <span className="cx-group-count">{count}</span><ChevronDown className={closed ? "closed" : ""} />
    </button>
  </div>;
}
