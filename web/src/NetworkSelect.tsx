import { useEffect, useState } from "react";
import { api } from "./api";

export type NetworkChoice = { mode: string; id?: string };
export type NetworkProfile = {
  id: string;
  name: string;
  version: number;
  httpProxy: string;
  httpsProxy: string;
  noProxy: string;
  username: string;
  hasPassword: boolean;
  revoked: boolean;
};

export function NetworkSelect({ value, onChange, "aria-labelledby": labelledBy, "aria-describedby": describedBy }: {
  value: NetworkChoice;
  onChange: (value: NetworkChoice) => void;
  "aria-labelledby"?: string;
  "aria-describedby"?: string;
}) {
  const [profiles, setProfiles] = useState<NetworkProfile[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    api<{ profiles: NetworkProfile[] }>("/networks")
      .then((result) => setProfiles(result.profiles))
      .catch((cause) => setError((cause as Error).message));
  }, []);
  return (
    <>
      <select
        aria-label={labelledBy ? undefined : "网络配置"}
        aria-labelledby={labelledBy}
        aria-describedby={describedBy}
        value={value.mode === "profile" ? value.id : value.mode || "default"}
        onChange={(event) => onChange(
          ["default", "direct"].includes(event.target.value)
            ? { mode: event.target.value }
            : { mode: "profile", id: event.target.value },
        )}
      >
        <option value="default">使用平台默认</option>
        <option value="direct">直连</option>
        {profiles.filter((profile) => !profile.revoked).map((profile) => (
          <option key={profile.id} value={profile.id}>{profile.name}</option>
        ))}
      </select>
      {error && <small role="alert">{error}</small>}
    </>
  );
}
