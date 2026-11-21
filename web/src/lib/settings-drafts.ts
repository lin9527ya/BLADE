type Field = {
  value: unknown;
  saved: unknown;
  revision: number;
  applied: number;
  pending: number;
  deps: string[];
  publish: (value: unknown) => void;
  writeOnly: boolean;
};
export type SettingsTicket =
  | { kind: "read"; seq: number; blocked: Set<string> }
  | { kind: "save"; seq: number; fields: Map<string, number> };

// UI drafts are independent of the last acknowledged server snapshot. Ticket
// ownership protects edits made during a save and unrelated sections. Writes
// are serialized so network completion order cannot reverse user intent.
export function createSettingsDrafts(changed?: () => void) {
  const fields = new Map<string, Field>();
  let seq = 0;
  let readonlyApplied = 0;
  let queue: Promise<unknown> = Promise.resolve();
  const applyField = (key: string, value: unknown, ticket: SettingsTicket) => {
    const field = fields.get(key);
    if (!field || ticket.seq < field.applied) return;
    if (ticket.kind === "read") {
      if (field.pending > 0 || ticket.blocked.has(key)) return;
      const dirty = !Object.is(field.value, field.saved);
      field.applied = ticket.seq;
      field.saved = value;
      if (!dirty) {
        field.value = value;
        field.publish(value);
      }
      changed?.();
    } else {
      const revision = ticket.fields.get(key);
      if (revision === undefined) return;
      field.applied = ticket.seq;
      field.saved = value;
      if (field.revision === revision) {
        field.value = value;
        field.publish(value);
      }
      changed?.();
    }
  };
  return {
    register(key: string, initial: unknown, deps: string[], publish: (value: unknown) => void, writeOnly = false) {
      const field = fields.get(key);
      if (field) {
        field.publish = publish;
        return;
      }
      fields.set(key, {
        value: initial,
        saved: initial,
        revision: 0,
        applied: 0,
        pending: 0,
        deps,
        publish,
        writeOnly,
      });
    },
    edit(key: string, update: unknown | ((current: unknown) => unknown)) {
      const field = fields.get(key)!;
      field.value = typeof update === "function" ? update(field.value) : update;
      field.revision++;
      field.publish(field.value);
      changed?.();
    },
    read(): SettingsTicket {
      return {
        kind: "read",
        seq: ++seq,
        blocked: new Set([...fields].filter(([, field]) => field.pending > 0).map(([key]) => key)),
      };
    },
    applyField,
    applyReadonly(ticket: SettingsTicket) {
      if (ticket.seq < readonlyApplied) return false;
      readonlyApplied = ticket.seq;
      return true;
    },
    dirty(_revision?: number) {
      return [...fields.values()].some((field) => !Object.is(field.value, field.saved));
    },
    submit<T>(
      patch: Record<string, unknown>,
      send: (patch: Record<string, unknown>, ticket: SettingsTicket) => Promise<T>,
    ): Promise<T> {
      const payload = { ...patch };
      const ticket: SettingsTicket & { kind: "save" } = { kind: "save", seq: ++seq, fields: new Map() };
      for (const [key, field] of fields) {
        const deps = field.deps.filter((dep) => Object.hasOwn(payload, dep));
        if (deps.length === 0) continue;
        // Clearing a stored key must not discard a different unsaved key draft.
        if (field.writeOnly && !deps.some((dep) => String(payload[dep]).trim() === String(field.value).trim()))
          continue;
        ticket.fields.set(key, field.revision);
        field.pending++;
      }
      const result = queue
        .then(() => send(payload, ticket))
        .then((value) => {
          for (const [key] of ticket.fields) {
            const field = fields.get(key)!;
            if (field.writeOnly) applyField(key, "", ticket);
          }
          return value;
        })
        .finally(() => {
          for (const [key] of ticket.fields) fields.get(key)!.pending--;
        });
      queue = result.catch(() => undefined);
      return result;
    },
  };
}
