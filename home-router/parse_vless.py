import json
from urllib.parse import urlparse, parse_qs, unquote

def parse_vless_to_outbound(vless_url):
    try:
        vless_url = vless_url.strip()
        parsed = urlparse(vless_url)
        if parsed.scheme != "vless":
            raise ValueError("Ссылка должна начинаться с vless://")

        user_id = parsed.username
        server_address = parsed.hostname
        server_port = parsed.port if parsed.port else 443

        # Получаем параметры (сразу берем первое значение из списка списков)
        params = {k: v[0] for k, v in parse_qs(parsed.query).items()}
        tag = unquote(parsed.fragment) if parsed.fragment else "proxy"

        network_type = params.get("type", "tcp")
        security_type = params.get("security", "none")

        # Базовый каркас outbound
        outbound = {
            "protocol": "vless",
            "tag": tag,
            "settings": {
                "vnext": [
                    {
                        "address": server_address,
                        "port": int(server_port),
                        "users": [
                            {
                                "id": user_id,
                                "flow": params.get("flow", ""),
                                "encryption": params.get("encryption", "none")
                            }
                        ]
                    }
                ]
            },
            "streamSettings": {
                "network": network_type,
                "security": security_type
            }
        }

        # 1. Специфичные настройки для XHTTP (Новый транспорт)
        if network_type == "xhttp":
            xhttp_settings = {}
            if "path" in params:
                xhttp_settings["path"] = unquote(params["path"])
            if "mode" in params:
                xhttp_settings["mode"] = params["mode"] # обычно 'stream-up', 'stream-one' и др.
            if "host" in params:
                xhttp_settings["host"] = params["host"]

            # Парсинг продвинутых параметров xhttp (extra / padding)
            extra_params = {}
            if "xPaddingBytes" in params:
                extra_params["xPaddingBytes"] = params["xPaddingBytes"]
            if "sc" in params: # speed control / congestion
                extra_params["speedControl"] = params["sc"]

            if extra_params:
                xhttp_settings["extra"] = extra_params

            outbound["streamSettings"]["xhttpSettings"] = xhttp_settings

        # 2. Настройки для REALITY
        if security_type == "reality":
            reality_settings = {
                "publicKey": params.get("pbk", ""),
                "fingerprint": params.get("fp", "chrome"),
                "serverName": params.get("sni", ""),
                "shortId": params.get("sid", ""),
                "spiderX": unquote(params.get("spx", "/"))
            }
            # Парсинг ALPN для XHTTP + REALITY (например, h3 для QUIC/HTTP3)
            if "alpn" in params:
                reality_settings["alpn"] = [a.strip() for a in params["alpn"].split(",")]

            outbound["streamSettings"]["realitySettings"] = reality_settings

        # 3. Настройки для классического TLS
        elif security_type == "tls":
            tls_settings = {
                "serverName": params.get("sni", ""),
                "fingerprint": params.get("fp", "chrome")
            }
            if "alpn" in params:
                tls_settings["alpn"] = [a.strip() for a in params["alpn"].split(",")]
            outbound["streamSettings"]["tlsSettings"] = tls_settings

        # 4. Совместимость с другими старыми протоколами (ws, grpc)
        if network_type == "grpc" and "serviceName" in params:
            outbound["streamSettings"]["grpcSettings"] = {"serviceName": params["serviceName"]}
        elif network_type == "ws":
            outbound["streamSettings"]["wsSettings"] = {
                "path": unquote(params.get("path", "/")),
                "headers": {"Host": params.get("sni", "")}
            }

        return outbound

    except Exception as e:
        print(f"❌ Ошибка разбора: {e}")
        return None

if __name__ == "__main__":
    link = input("Вставьте вашу ссылку vless:// \n> ")
    config_fragment = parse_vless_to_outbound(link)

    if config_fragment:
        print("\n=== Готовый блок outbound для Xray (с поддержкой xhttp) ===")
        print(json.dumps(config_fragment, indent=2, ensure_ascii=False))
        print("==========================================================")
