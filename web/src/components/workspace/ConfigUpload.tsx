import { useState, useRef } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Spinner } from '@/components/ui/spinner';
import { Upload, CheckCircle, FileArchive } from 'lucide-react';
import { api } from '@/api/client';

interface Props {
  workspaceId: string;
  currentConfigVersion?: string;
}

export function ConfigUpload({ workspaceId, currentConfigVersion }: Props) {
  const queryClient = useQueryClient();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [dragOver, setDragOver] = useState(false);

  const uploadMutation = useMutation({
    mutationFn: async (file: File) => {
      const formData = new FormData();
      formData.append('file', file);

      // Config archives can be large, so this call carries its own deadline.
      // The client applies its 30s default only to a call that supplies no
      // signal, so this one replaces that deadline rather than racing it.
      const { data, error } = await api.POST('/workspaces/{workspaceId}/upload', {
        params: { path: { workspaceId } },
        // The generated body type is the object of multipart parts —
        // `{ file: string }`, because OpenAPI's `format: binary` has no
        // TypeScript counterpart. FormData is what carries those parts on the
        // wire: openapi-fetch forwards a FormData body untouched and leaves
        // Content-Type unset so the browser writes the multipart boundary.
        body: formData as unknown as { file: string },
        signal: AbortSignal.timeout(120_000),
      });

      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId] });
      toast.success('Configuration uploaded');
    },
    onError: (e) => toast.error((e as { error?: string })?.error ?? 'Upload failed'),
  });

  const handleFile = (file: File) => {
    if (!file.name.endsWith('.tar.gz') && !file.name.endsWith('.tgz')) {
      toast.error('Please upload a .tar.gz archive');
      return;
    }
    uploadMutation.mutate(file);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(false);
    const file = e.dataTransfer.files[0];
    if (file) handleFile(file);
  };

  return (
    <div className="space-y-3">
      {/* Drop target is pointer/drag only; keyboard users use the file input below. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: native file drop zone */}
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setDragOver(true);
        }}
        onDragLeave={() => setDragOver(false)}
        onDrop={handleDrop}
        className={`rounded-lg border-2 border-dashed p-6 text-center transition-colors ${
          dragOver ? 'border-primary bg-primary/5' : 'border-border hover:border-primary/30'
        }`}
      >
        {uploadMutation.isPending ? (
          <div className="flex flex-col items-center gap-2">
            <Spinner className="w-6 h-6" />
            <p className="text-sm text-muted-foreground">Uploading...</p>
          </div>
        ) : (
          <>
            <Upload className="w-8 h-8 text-muted-foreground mx-auto mb-2" />
            <p className="text-sm font-medium mb-1">Drop a .tar.gz archive here</p>
            <p className="text-xs text-muted-foreground mb-3">
              Archive should contain your .tf configuration files
            </p>
            <Button size="sm" variant="outline" onClick={() => fileInputRef.current?.click()}>
              <FileArchive className="w-3.5 h-3.5" />
              Choose file
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept="*/*"
              className="hidden"
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file) handleFile(file);
                e.target.value = '';
              }}
            />
          </>
        )}
      </div>

      {currentConfigVersion && (
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <CheckCircle className="w-3.5 h-3.5 text-success" />
          <span>
            Current version: <code className="font-mono">{currentConfigVersion.slice(0, 8)}</code>
          </span>
        </div>
      )}
    </div>
  );
}
