export type ProjectRole = "viewer" | "operator" | "owner";

export interface Principal {
  id: string;
  username: string;
  email?: string;
  displayName?: string;
  platformAdmin: boolean;
  breakGlass?: boolean;
}

export function canManageProject(role?: ProjectRole, admin = false) {
  return admin || role === "owner";
}

export function canOperateProject(role?: ProjectRole, admin = false) {
  return canManageProject(role, admin) || role === "operator";
}
