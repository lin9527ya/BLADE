"use client";

import * as React from "react";

import { CheckIcon, LinkIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/utils";

// CopyLinkButton 复制当前地址栏（含筛选参数），是 URL 化筛选的出口。
export function CopyLinkButton({ label = "复制链接" }: { label?: string }) {
  const [copied, setCopied] = React.useState(false);
  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  React.useEffect(() => {
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  async function copy() {
    const ok = await copyText(window.location.href);
    if (!ok) {
      toast.error("复制失败，请手动复制地址栏");
      return;
    }
    setCopied(true);
    toast.success("当前视图链接已复制");
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }

  return (
    <Button type="button" size="sm" variant="outline" onClick={() => void copy()}>
      {copied ? <CheckIcon className="size-4" /> : <LinkIcon className="size-4" />}
      {label}
    </Button>
  );
}
