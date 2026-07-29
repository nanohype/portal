import { useMutation, useQueryClient } from '@tanstack/react-query';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { useId, useState } from 'react';
import { toast } from 'sonner';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';

const AWS_ACCOUNT_RE = /^\d{12}$/;
const AWS_ROLE_ARN_RE = /^arn:aws[a-z-]*:iam::(\d{12}):role\/.+$/;
const AWS_REGION_RE = /^[a-z]{2}-[a-z]+-\d$/;
// The substrate prerequisites are three different resource types — an IAM role,
// two IAM policies and a KMS key — so this asserts the ARN grammar and a
// plausible service rather than pretending to know each one's path. Mirrors
// checkSubstrateARNs on the server, which is the real gate.
const SUBSTRATE_ARN_RE = /^arn:aws[a-z-]*:(iam|kms):[a-z0-9-]*:\d{12}:.+$/;

export function AccountCreateModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient();
  const uid = useId();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [awsAccountID, setAwsAccountID] = useState('');
  const [assumeRoleARN, setAssumeRoleARN] = useState('');
  const [externalID, setExternalID] = useState('');
  const [defaultRegion, setDefaultRegion] = useState('');
  const [showExternalID, setShowExternalID] = useState(false);
  const [vendRoleARN, setVendRoleARN] = useState('');
  const [dataKmsKeyARN, setDataKmsKeyARN] = useState('');
  const [clusterBoundaryARN, setClusterBoundaryARN] = useState('');
  const [operatorBoundaryARN, setOperatorBoundaryARN] = useState('');
  const [showSubstrate, setShowSubstrate] = useState(false);

  const reset = () => {
    setName('');
    setDescription('');
    setAwsAccountID('');
    setAssumeRoleARN('');
    setExternalID('');
    setDefaultRegion('');
    setShowExternalID(false);
    setVendRoleARN('');
    setDataKmsKeyARN('');
    setClusterBoundaryARN('');
    setOperatorBoundaryARN('');
    setShowSubstrate(false);
  };

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/accounts', {
        body: {
          name: name.trim(),
          description: description.trim() || undefined,
          aws_account_id: awsAccountID.trim(),
          assume_role_arn: assumeRoleARN.trim(),
          external_id: externalID.trim() || undefined,
          default_region: defaultRegion.trim(),
          vend_role_arn: vendRoleARN.trim() || undefined,
          data_kms_key_arn: dataKmsKeyARN.trim() || undefined,
          cluster_permissions_boundary_arn: clusterBoundaryARN.trim() || undefined,
          operator_permissions_boundary_arn: operatorBoundaryARN.trim() || undefined,
        },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['accounts'] });
      toast.success('Account created');
      reset();
      onClose();
    },
    onError: (e: unknown) => {
      const msg = (e as { message?: string })?.message ?? 'Failed to create account';
      toast.error(msg);
    },
  });

  const arnAccountMatch = (() => {
    const m = assumeRoleARN.match(AWS_ROLE_ARN_RE);
    return m?.[1];
  })();

  const substrateShape = (v: string) =>
    v.trim() && !SUBSTRATE_ARN_RE.test(v.trim())
      ? 'Must look like arn:aws:<iam|kms>:<region>:<account>:<resource>'
      : null;

  // A vend role is only usable with both permissions boundaries, and the order
  // path refuses the combination. Saying so here means the operator learns at
  // registration rather than at the first cross-account vend.
  const vendRoleNeedsBoundaries =
    !!vendRoleARN.trim() && (!clusterBoundaryARN.trim() || !operatorBoundaryARN.trim());

  const errors = {
    name: !name.trim() ? 'Required' : null,
    awsAccountID:
      awsAccountID && !AWS_ACCOUNT_RE.test(awsAccountID.trim())
        ? 'Must be exactly 12 digits'
        : !awsAccountID
          ? 'Required'
          : null,
    assumeRoleARN:
      assumeRoleARN && !AWS_ROLE_ARN_RE.test(assumeRoleARN.trim())
        ? 'Must look like arn:aws:iam::<account>:role/<name>'
        : !assumeRoleARN
          ? 'Required'
          : arnAccountMatch && arnAccountMatch !== awsAccountID.trim()
            ? 'Account in ARN does not match aws_account_id above'
            : null,
    defaultRegion:
      defaultRegion && !AWS_REGION_RE.test(defaultRegion.trim())
        ? 'Must look like us-west-2'
        : !defaultRegion
          ? 'Required'
          : null,
    vendRoleARN: substrateShape(vendRoleARN),
    dataKmsKeyARN: substrateShape(dataKmsKeyARN),
    clusterBoundaryARN: substrateShape(clusterBoundaryARN),
    operatorBoundaryARN: substrateShape(operatorBoundaryARN),
  };

  const canSubmit =
    !errors.name &&
    !errors.awsAccountID &&
    !errors.assumeRoleARN &&
    !errors.defaultRegion &&
    !errors.vendRoleARN &&
    !errors.dataKmsKeyARN &&
    !errors.clusterBoundaryARN &&
    !errors.operatorBoundaryARN &&
    !vendRoleNeedsBoundaries;

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogContent className="max-h-[80vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>New Account</DialogTitle>
          <DialogDescription>
            Register an AWS account by giving portal an assume-role to use.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Field label="Name" htmlFor={`${uid}-name`} error={touched('name', name, errors.name)}>
            <Input
              id={`${uid}-name`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. production"
              autoFocus
            />
          </Field>

          <Field label="Description (optional)" htmlFor={`${uid}-description`}>
            <Input
              id={`${uid}-description`}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What lives in this account?"
            />
          </Field>

          <Field
            label="AWS Account ID"
            htmlFor={`${uid}-aws-account-id`}
            error={touched('awsAccountID', awsAccountID, errors.awsAccountID)}
          >
            <Input
              id={`${uid}-aws-account-id`}
              value={awsAccountID}
              onChange={(e) => setAwsAccountID(e.target.value)}
              placeholder="123456789012"
              className="font-mono"
            />
          </Field>

          <Field
            label="Assume Role ARN"
            htmlFor={`${uid}-assume-role-arn`}
            error={touched('assumeRoleARN', assumeRoleARN, errors.assumeRoleARN)}
          >
            <Input
              id={`${uid}-assume-role-arn`}
              value={assumeRoleARN}
              onChange={(e) => setAssumeRoleARN(e.target.value)}
              placeholder="arn:aws:iam::123456789012:role/portal-cross-account"
              className="font-mono"
            />
          </Field>

          <Field
            label="Default Region"
            htmlFor={`${uid}-default-region`}
            error={touched('defaultRegion', defaultRegion, errors.defaultRegion)}
          >
            <Input
              id={`${uid}-default-region`}
              value={defaultRegion}
              onChange={(e) => setDefaultRegion(e.target.value)}
              placeholder="us-west-2"
              className="font-mono"
            />
          </Field>

          <div>
            <button
              type="button"
              onClick={() => setShowExternalID(!showExternalID)}
              className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            >
              {showExternalID ? (
                <ChevronDown className="w-3 h-3" />
              ) : (
                <ChevronRight className="w-3 h-3" />
              )}
              External ID (optional)
            </button>
            {showExternalID && (
              <div className="mt-2">
                <Input
                  type="password"
                  aria-label="External ID"
                  value={externalID}
                  onChange={(e) => setExternalID(e.target.value)}
                  placeholder="Shared secret used in the role's trust policy"
                />
                <p className="text-[11px] text-muted-foreground/70 mt-1">
                  Stored encrypted. Required only when the assume-role trust policy includes an{' '}
                  <span className="font-mono">sts:ExternalId</span> condition.
                </p>
              </div>
            )}
          </div>

          <div>
            <button
              type="button"
              onClick={() => setShowSubstrate(!showSubstrate)}
              className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
            >
              {showSubstrate ? (
                <ChevronDown className="w-3 h-3" />
              ) : (
                <ChevronRight className="w-3 h-3" />
              )}
              Substrate prerequisites (optional)
            </button>
            {showSubstrate && (
              <div className="mt-2 space-y-3">
                <p className="text-[11px] text-muted-foreground/70">
                  References landing-zone publishes to SSM for this account. portal stamps them onto
                  every cluster order into it; the vend does not create them. Leave blank for
                  same-account vending — empty means ungated.
                </p>

                <Field
                  label="Fleet-vend Role ARN"
                  htmlFor={`${uid}-vend-role-arn`}
                  error={errors.vendRoleARN}
                >
                  <Input
                    id={`${uid}-vend-role-arn`}
                    value={vendRoleARN}
                    onChange={(e) => setVendRoleARN(e.target.value)}
                    placeholder="arn:aws:iam::123456789012:role/production-fleet-vend"
                    className="font-mono"
                  />
                </Field>

                <Field
                  label="Cluster Permissions Boundary ARN"
                  htmlFor={`${uid}-cluster-boundary-arn`}
                  error={errors.clusterBoundaryARN}
                >
                  <Input
                    id={`${uid}-cluster-boundary-arn`}
                    value={clusterBoundaryARN}
                    onChange={(e) => setClusterBoundaryARN(e.target.value)}
                    placeholder="arn:aws:iam::123456789012:policy/production-cluster-boundary"
                    className="font-mono"
                  />
                </Field>

                <Field
                  label="Operator Permissions Boundary ARN"
                  htmlFor={`${uid}-operator-boundary-arn`}
                  error={errors.operatorBoundaryARN}
                >
                  <Input
                    id={`${uid}-operator-boundary-arn`}
                    value={operatorBoundaryARN}
                    onChange={(e) => setOperatorBoundaryARN(e.target.value)}
                    placeholder="arn:aws:iam::123456789012:policy/production-operator-boundary"
                    className="font-mono"
                  />
                </Field>

                {vendRoleNeedsBoundaries && (
                  <p className="text-[11px] text-destructive">
                    A fleet-vend role needs both permissions boundaries. A cross-account vend mints
                    IAM under them, and an order carrying the role without both is refused.
                  </p>
                )}

                <Field
                  label="Data KMS Key ARN"
                  htmlFor={`${uid}-data-kms-key-arn`}
                  error={errors.dataKmsKeyARN}
                >
                  <Input
                    id={`${uid}-data-kms-key-arn`}
                    value={dataKmsKeyARN}
                    onChange={(e) => setDataKmsKeyARN(e.target.value)}
                    placeholder="arn:aws:kms:us-west-2:123456789012:key/abcd-1234"
                    className="font-mono"
                  />
                </Field>
              </div>
            )}
          </div>

          <div className="flex justify-end gap-2 pt-3 border-t border-border/40">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => createMutation.mutate()}
              disabled={!canSubmit || createMutation.isPending}
            >
              {createMutation.isPending ? 'Creating...' : 'Create Account'}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Field({
  label,
  htmlFor,
  error,
  children,
}: {
  label: string;
  htmlFor: string;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label htmlFor={htmlFor} className="text-xs font-medium text-muted-foreground mb-1.5 block">
        {label}
      </label>
      {children}
      {error && <p className="text-[11px] text-destructive mt-1">{error}</p>}
    </div>
  );
}

// Only show validation errors after the user has touched the field
// (empty + untouched should not render a red "Required").
function touched(_key: string, value: string, error: string | null): string | null {
  if (value === '') return null;
  return error;
}
