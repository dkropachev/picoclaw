import {
  IconBell,
  IconBellOff,
  IconDeviceMobile,
  IconEdit,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"

import {
  NotificationAPIError,
  type PushSubscriptionDevice,
  createNotificationRequestID,
  createPushSubscriptionDevice,
  deletePushSubscriptionDevice,
  getNotificationSettings,
  listPushSubscriptionDevices,
  updateNotificationSettings,
  updatePushSubscriptionDevice,
} from "@/api/notifications"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import {
  subscribeBrowserToPush,
  supportsPicoClawPush,
  unsubscribeBrowserFromPush,
} from "@/lib/pwa-notifications"

const currentPushDeviceStorageKey = "picoclaw.push-device-id"

export function PushNotificationSettings({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const queryClient = useQueryClient()
  const [deviceName, setDeviceName] = useState("This device")
  const [editingDevice, setEditingDevice] = useState<PushSubscriptionDevice>()
  const [editingName, setEditingName] = useState("")
  const [error, setError] = useState("")
  const supported = supportsPicoClawPush()

  const settingsQuery = useQuery({
    queryKey: ["notification-settings"],
    queryFn: ({ signal }) => getNotificationSettings(signal),
    enabled: open,
  })
  const devicesQuery = useQuery({
    queryKey: ["push-subscriptions"],
    queryFn: ({ signal }) => listPushSubscriptionDevices(signal),
    enabled: open,
  })
  const devices = devicesQuery.data?.subscriptions ?? []

  useEffect(() => {
    if (!editingDevice) return
    setEditingName(editingDevice.name)
  }, [editingDevice])

  const enableMutation = useMutation({
    mutationFn: async () => {
      const publicKey = settingsQuery.data?.vapid_public_key
      if (!publicKey) throw new Error("Push server is not configured.")
      const subscription = await subscribeBrowserToPush(publicKey)
      return createPushSubscriptionDevice({
        ...subscription,
        name: deviceName.trim() || "This device",
        request_id: createNotificationRequestID(),
      })
    },
    onSuccess: (device) => {
      globalThis.localStorage?.setItem(currentPushDeviceStorageKey, device.id)
      setError("")
      void queryClient.invalidateQueries({ queryKey: ["push-subscriptions"] })
    },
    onError: (reason) => setError(pushErrorMessage(reason)),
  })
  const settingsMutation = useMutation({
    mutationFn: (includeRepository: boolean) =>
      updateNotificationSettings({
        include_repository_in_push: includeRepository,
        expected_version: settingsQuery.data?.version ?? 0,
        request_id: createNotificationRequestID(),
      }),
    onSuccess: (settings) => {
      setError("")
      queryClient.setQueryData(["notification-settings"], settings)
    },
    onError: (reason) => {
      setError(pushErrorMessage(reason))
      void settingsQuery.refetch()
    },
  })
  const deviceMutation = useMutation({
    mutationFn: ({
      device,
      name,
      enabled,
    }: {
      device: PushSubscriptionDevice
      name: string
      enabled: boolean
    }) =>
      updatePushSubscriptionDevice(device.id, {
        name,
        enabled,
        expected_version: device.version,
        request_id: createNotificationRequestID(),
      }),
    onSuccess: () => {
      setEditingDevice(undefined)
      setError("")
      void queryClient.invalidateQueries({ queryKey: ["push-subscriptions"] })
    },
    onError: (reason) => {
      setError(pushErrorMessage(reason))
      void devicesQuery.refetch()
    },
  })
  const revokeMutation = useMutation({
    mutationFn: async (device: PushSubscriptionDevice) => {
      await deletePushSubscriptionDevice(device.id, {
        expected_version: device.version,
        request_id: createNotificationRequestID(),
      })
      if (
        globalThis.localStorage?.getItem(currentPushDeviceStorageKey) ===
        device.id
      ) {
        await unsubscribeBrowserFromPush()
        globalThis.localStorage?.removeItem(currentPushDeviceStorageKey)
      }
    },
    onSuccess: () => {
      setError("")
      void queryClient.invalidateQueries({ queryKey: ["push-subscriptions"] })
    },
    onError: (reason) => {
      setError(pushErrorMessage(reason))
      void devicesQuery.refetch()
    },
  })

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="max-h-[90dvh] overflow-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>Mobile notifications</DialogTitle>
            <DialogDescription>
              Send critical and high-priority action requests to installed
              devices.
            </DialogDescription>
          </DialogHeader>

          {error && (
            <p
              role="alert"
              className="bg-destructive/5 text-destructive rounded-md p-3 text-sm"
            >
              {error}
            </p>
          )}

          {!supported ? (
            <div className="border-border text-muted-foreground flex items-start gap-3 rounded-lg border p-4 text-sm">
              <IconBellOff className="mt-0.5 size-5 shrink-0" />
              <p>
                Push is unavailable here. Use HTTPS and an installed PWA or a
                browser with Web Push support. The in-app inbox remains
                available.
              </p>
            </div>
          ) : (
            <div className="border-border space-y-3 rounded-lg border p-4">
              <div>
                <Label htmlFor="push-device-name">Device name</Label>
                <Input
                  id="push-device-name"
                  value={deviceName}
                  maxLength={80}
                  className="mt-2"
                  onChange={(event) => setDeviceName(event.target.value)}
                />
              </div>
              <Button
                type="button"
                disabled={
                  enableMutation.isPending ||
                  settingsQuery.isPending ||
                  !settingsQuery.data?.vapid_public_key
                }
                onClick={() => enableMutation.mutate()}
              >
                <IconBell />
                {enableMutation.isPending
                  ? "Enabling…"
                  : "Enable mobile notifications"}
              </Button>
              <p className="text-muted-foreground text-xs">
                Permission is requested only after this button is pressed.
              </p>
            </div>
          )}

          <div className="border-border flex items-center justify-between gap-4 rounded-lg border p-4">
            <div>
              <p className="text-sm font-medium">
                Show repository on lock screen
              </p>
              <p className="text-muted-foreground text-xs">
                Off keeps push content privacy-minimal.
              </p>
            </div>
            <Switch
              aria-label="Show repository on lock screen"
              checked={settingsQuery.data?.include_repository_in_push ?? false}
              disabled={settingsMutation.isPending || !settingsQuery.data}
              onCheckedChange={(checked) => settingsMutation.mutate(checked)}
            />
          </div>

          <section className="space-y-2" aria-labelledby="push-devices-heading">
            <div className="flex items-center justify-between gap-3">
              <h3 id="push-devices-heading" className="text-sm font-medium">
                Registered devices
              </h3>
              <Badge variant="secondary">{devices.length}</Badge>
            </div>
            {devicesQuery.isPending ? (
              <p className="text-muted-foreground py-4 text-sm">
                Loading devices…
              </p>
            ) : devicesQuery.isError ? (
              <p className="text-destructive py-4 text-sm">
                Could not load devices.
              </p>
            ) : devices.length === 0 ? (
              <p className="text-muted-foreground border-border rounded-lg border border-dashed py-6 text-center text-sm">
                No devices registered.
              </p>
            ) : (
              <div className="divide-border divide-y rounded-lg border">
                {devices.map((device) => (
                  <div
                    key={device.id}
                    className="flex min-w-0 items-center gap-3 p-3"
                  >
                    <IconDeviceMobile className="text-muted-foreground size-5 shrink-0" />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">
                        {device.name}
                      </p>
                      <p className="text-muted-foreground text-xs">
                        {device.enabled ? "Enabled" : "Disabled"}
                        {device.last_delivered_at
                          ? ` · Last push ${new Date(device.last_delivered_at).toLocaleString()}`
                          : ""}
                      </p>
                    </div>
                    <Switch
                      aria-label={`${device.enabled ? "Disable" : "Enable"} ${device.name}`}
                      checked={device.enabled}
                      disabled={deviceMutation.isPending}
                      onCheckedChange={(enabled) =>
                        deviceMutation.mutate({
                          device,
                          name: device.name,
                          enabled,
                        })
                      }
                    />
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Rename ${device.name}`}
                      onClick={() => setEditingDevice(device)}
                    >
                      <IconEdit />
                    </Button>
                    <Button
                      type="button"
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Revoke ${device.name}`}
                      disabled={revokeMutation.isPending}
                      onClick={() => revokeMutation.mutate(device)}
                    >
                      <IconTrash />
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </section>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(editingDevice)}
        onOpenChange={(next) => !next && setEditingDevice(undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rename device</DialogTitle>
            <DialogDescription>
              Use a name that identifies this browser.
            </DialogDescription>
          </DialogHeader>
          <div>
            <Label htmlFor="push-device-rename">Device name</Label>
            <Input
              id="push-device-rename"
              value={editingName}
              maxLength={80}
              className="mt-2"
              onChange={(event) => setEditingName(event.target.value)}
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              disabled={
                !editingDevice ||
                !editingName.trim() ||
                deviceMutation.isPending
              }
              onClick={() =>
                editingDevice &&
                deviceMutation.mutate({
                  device: editingDevice,
                  name: editingName.trim(),
                  enabled: editingDevice.enabled,
                })
              }
            >
              Save name
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function pushErrorMessage(error: unknown): string {
  if (error instanceof NotificationAPIError) return error.message
  return error instanceof Error
    ? error.message
    : "Push notification action failed."
}
