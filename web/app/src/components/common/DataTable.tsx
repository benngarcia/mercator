import * as React from "react";
import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";

import { cn } from "@/lib/utils";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";

export interface Column<T> {
  /** Stable id; also the default sort key. */
  id: string;
  header: React.ReactNode;
  /** Cell renderer. */
  cell: (row: T) => React.ReactNode;
  /** Per-column alignment. Defaults to left. */
  align?: "left" | "right" | "center";
  /** Make the column sortable. Provide an accessor for non-trivial values. */
  sortable?: boolean;
  /** Value used for sorting; defaults to the cell when it is a primitive. */
  sortValue?: (row: T) => string | number | boolean | null | undefined;
  /** Optional fixed/min width utility classes. */
  className?: string;
  headerClassName?: string;
}

export interface DataTableProps<T> {
  columns: Column<T>[];
  data: T[];
  rowKey: (row: T) => string;
  onRowClick?: (row: T) => void;
  isLoading?: boolean;
  /** Rendered (full-width) when not loading and data is empty. */
  emptyState?: React.ReactNode;
  /** Mark a row selected (e.g. master/detail). */
  selectedKey?: string;
  className?: string;
  /** Skeleton row count while loading. Defaults to 6. */
  skeletonRows?: number;
}

type SortDir = "asc" | "desc";

const alignClass: Record<NonNullable<Column<unknown>["align"]>, string> = {
  left: "text-left",
  right: "text-right",
  center: "text-center",
};

function defaultSortValue<T>(
  col: Column<T>,
  row: T,
): string | number | boolean | null | undefined {
  if (col.sortValue) return col.sortValue(row);
  const raw = (row as Record<string, unknown>)[col.id];
  if (
    raw === null ||
    raw === undefined ||
    typeof raw === "string" ||
    typeof raw === "number" ||
    typeof raw === "boolean"
  ) {
    return raw;
  }
  return undefined;
}

function compare(
  a: string | number | boolean | null | undefined,
  b: string | number | boolean | null | undefined,
): number {
  const an = a === null || a === undefined;
  const bn = b === null || b === undefined;
  if (an && bn) return 0;
  if (an) return 1; // nullish sort last
  if (bn) return -1;
  if (typeof a === "number" && typeof b === "number") return a - b;
  return String(a).localeCompare(String(b), undefined, { numeric: true });
}

/**
 * DataTable is the dense, operator-grade table used everywhere. Columns are
 * declarative; sortable columns toggle asc/desc/none on header click. Rows are
 * keyboard-navigable (arrow keys move focus, Enter/Space activate) when
 * `onRowClick` is provided, and skeleton rows render while `isLoading`.
 */
export function DataTable<T>({
  columns,
  data,
  rowKey,
  onRowClick,
  isLoading,
  emptyState,
  selectedKey,
  className,
  skeletonRows = 6,
}: DataTableProps<T>) {
  const [sort, setSort] = React.useState<{ id: string; dir: SortDir } | null>(
    null,
  );
  const bodyRef = React.useRef<HTMLTableSectionElement>(null);

  const sorted = React.useMemo(() => {
    if (!sort) return data;
    const col = columns.find((c) => c.id === sort.id);
    if (!col) return data;
    const factor = sort.dir === "asc" ? 1 : -1;
    return [...data].sort(
      (a, b) =>
        factor * compare(defaultSortValue(col, a), defaultSortValue(col, b)),
    );
  }, [data, sort, columns]);

  const onSort = React.useCallback((id: string) => {
    setSort((prev) => {
      if (!prev || prev.id !== id) return { id, dir: "asc" };
      if (prev.dir === "asc") return { id, dir: "desc" };
      return null; // third click clears
    });
  }, []);

  const onRowKeyDown = React.useCallback(
    (event: React.KeyboardEvent<HTMLTableRowElement>, row: T) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        onRowClick?.(row);
        return;
      }
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const rows = bodyRef.current?.querySelectorAll<HTMLTableRowElement>(
          'tr[data-row="true"]',
        );
        if (!rows) return;
        const list = Array.from(rows);
        const idx = list.indexOf(event.currentTarget);
        const next =
          event.key === "ArrowDown"
            ? Math.min(idx + 1, list.length - 1)
            : Math.max(idx - 1, 0);
        list[next]?.focus();
      }
    },
    [onRowClick],
  );

  const showEmpty = !isLoading && sorted.length === 0;

  return (
    <div
      className={cn(
        "overflow-hidden rounded-lg border border-border bg-card",
        className,
      )}
    >
      <Table>
        <TableHeader className="bg-muted/40">
          <TableRow className="hover:bg-transparent">
            {columns.map((col) => {
              const active = sort?.id === col.id;
              const SortIcon = !active
                ? ChevronsUpDown
                : sort.dir === "asc"
                  ? ArrowUp
                  : ArrowDown;
              return (
                <TableHead
                  key={col.id}
                  className={cn(
                    "h-9 whitespace-nowrap text-[0.6875rem] font-medium uppercase tracking-wider",
                    alignClass[col.align ?? "left"],
                    col.headerClassName,
                  )}
                  aria-sort={
                    active
                      ? sort.dir === "asc"
                        ? "ascending"
                        : "descending"
                      : undefined
                  }
                >
                  {col.sortable ? (
                    <button
                      type="button"
                      onClick={() => onSort(col.id)}
                      className={cn(
                        "inline-flex items-center gap-1 rounded transition-colors hover:text-foreground",
                        col.align === "right" && "flex-row-reverse",
                        active && "text-foreground",
                      )}
                    >
                      {col.header}
                      <SortIcon
                        className={cn(
                          "size-3 shrink-0",
                          active ? "text-primary" : "text-muted-foreground/60",
                        )}
                      />
                    </button>
                  ) : (
                    col.header
                  )}
                </TableHead>
              );
            })}
          </TableRow>
        </TableHeader>
        <TableBody ref={bodyRef}>
          {isLoading
            ? Array.from({ length: skeletonRows }).map((_, r) => (
                <TableRow key={`sk-${r}`} className="hover:bg-transparent">
                  {columns.map((col) => (
                    <TableCell key={col.id} className={col.className}>
                      <Skeleton
                        className={cn(
                          "h-4",
                          col.align === "right" ? "ml-auto w-12" : "w-24",
                        )}
                      />
                    </TableCell>
                  ))}
                </TableRow>
              ))
            : showEmpty
              ? (
                  <TableRow className="hover:bg-transparent">
                    <TableCell
                      colSpan={columns.length}
                      className="p-0"
                    >
                      {emptyState ?? (
                        <div className="py-12 text-center text-sm text-muted-foreground">
                          No rows
                        </div>
                      )}
                    </TableCell>
                  </TableRow>
                )
              : sorted.map((row) => {
                  const key = rowKey(row);
                  const selected = selectedKey === key;
                  const clickable = Boolean(onRowClick);
                  return (
                    <TableRow
                      key={key}
                      data-row="true"
                      data-state={selected ? "selected" : undefined}
                      tabIndex={clickable ? 0 : undefined}
                      role={clickable ? "button" : undefined}
                      onClick={
                        clickable ? () => onRowClick?.(row) : undefined
                      }
                      onKeyDown={
                        clickable
                          ? (e) => onRowKeyDown(e, row)
                          : undefined
                      }
                      className={cn(
                        clickable &&
                          "cursor-pointer focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-ring",
                        selected && "bg-accent/40",
                      )}
                    >
                      {columns.map((col) => (
                        <TableCell
                          key={col.id}
                          className={cn(
                            "py-2 align-middle",
                            alignClass[col.align ?? "left"],
                            col.className,
                          )}
                        >
                          {col.cell(row)}
                        </TableCell>
                      ))}
                    </TableRow>
                  );
                })}
        </TableBody>
      </Table>
    </div>
  );
}
