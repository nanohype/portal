import { useState, useRef } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Spinner } from '@/components/ui/spinner';
import { Upload, CheckCircle, FileArchive } from 'lucide-react';

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

      const token = localStorage.getItem('portal_token');
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/upload`, {
        method: 'POST',
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        body: formData,
        // Config archives can be large; wider deadline than the 30s API default.
        signal: AbortSignal.timeout(120_000),
      });

      if (!res.ok) {
        const err = await res.json().catch(() => ({ error: 'Upload failed' }));
        throw new Error(err.error || 'Upload failed');
      }

      return res.json();
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId] });
      toast.success('Configuration uploaded');
    },
    onError: (err: Error) => toast.error(err.message),
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
