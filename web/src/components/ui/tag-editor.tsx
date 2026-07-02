import { useState } from 'react';
import { Input } from '@/components/ui/input';
import { X, Plus } from 'lucide-react';

interface TagEditorProps {
  value: string;
  onChange: (value: string) => void;
}

function parseTags(value: string): Record<string, string> {
  try {
    const parsed = JSON.parse(value);
    if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
      const result: Record<string, string> = {};
      for (const [k, v] of Object.entries(parsed)) {
        result[k] = String(v);
      }
      return result;
    }
  } catch {
    // fall through to return {}
  }
  return {};
}

export function TagEditor({ value, onChange }: TagEditorProps) {
  const [newKey, setNewKey] = useState('');
  const [newValue, setNewValue] = useState('');

  const tags = parseTags(value);

  const update = (newTags: Record<string, string>) => {
    onChange(JSON.stringify(newTags));
  };

  const addTag = () => {
    if (!newKey.trim()) return;
    const updated = { ...tags, [newKey.trim()]: newValue.trim() };
    update(updated);
    setNewKey('');
    setNewValue('');
  };

  const removeTag = (key: string) => {
    const updated = { ...tags };
    delete updated[key];
    update(updated);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      addTag();
    }
  };

  const entries = Object.entries(tags);

  return (
    <div className="space-y-2">
      {entries.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {entries.map(([k, v]) => (
            <span
              key={k}
              className="inline-flex items-center gap-1 rounded-md border border-border bg-input px-2 py-1 text-xs font-mono"
            >
              <span className="text-primary">{k}</span>
              <span className="text-muted-foreground/50">:</span>
              <span className="text-foreground">{v}</span>
              <button
                type="button"
                aria-label={`Remove tag ${k}`}
                onClick={() => removeTag(k)}
                className="ml-0.5 p-0.5 rounded hover:bg-destructive/10 text-muted-foreground/50 hover:text-destructive transition-colors cursor-pointer"
              >
                <X className="w-2.5 h-2.5" />
              </button>
            </span>
          ))}
        </div>
      )}
      <div className="flex items-center gap-1.5">
        <Input
          placeholder="key"
          value={newKey}
          onChange={(e) => setNewKey(e.target.value)}
          onKeyDown={handleKeyDown}
          className="h-7 text-xs font-mono flex-1"
        />
        <Input
          placeholder="value"
          value={newValue}
          onChange={(e) => setNewValue(e.target.value)}
          onKeyDown={handleKeyDown}
          className="h-7 text-xs font-mono flex-1"
        />
        <button
          type="button"
          aria-label="Add tag"
          onClick={addTag}
          disabled={!newKey.trim()}
          className="p-1 rounded-md border border-border hover:bg-hover text-muted-foreground hover:text-foreground disabled:opacity-30 transition-colors cursor-pointer"
        >
          <Plus className="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  );
}
