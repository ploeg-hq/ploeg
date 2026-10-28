import ipaddress
import sys

import yaml

ADMITTED = 0
DENIED = 1
UNUSABLE = 2


def policy_named(documents, name):
    for document in documents:
        if document and document.get("kind") == "NetworkPolicy" and document["metadata"]["name"] == name:
            return document
    return None


def port_matches(rule, protocol, port):
    ports = rule.get("ports")
    if not ports:
        return True
    for entry in ports:
        if entry.get("protocol", "TCP") != protocol:
            continue
        if "port" not in entry:
            return True
        low = int(entry["port"])
        high = int(entry.get("endPort", low))
        if low <= port <= high:
            return True
    return False


def ip_block_admits(block, address):
    if address not in ipaddress.ip_network(block["cidr"]):
        return False
    return not any(address in ipaddress.ip_network(excepted) for excepted in block.get("except") or [])


def peers_admit(rule, address):
    if not rule.get("to"):
        return True
    return any("ipBlock" in peer and ip_block_admits(peer["ipBlock"], address) for peer in rule["to"])


def egress_admits(policy, address, protocol, port):
    return any(port_matches(rule, protocol, port) and peers_admit(rule, address) for rule in policy["spec"].get("egress") or [])


def main(argv):
    if len(argv) not in (5, 6):
        print("usage: netpol-admits.py <rendered.yaml> <policy-name> <ipv4-address> <port> [TCP|UDP]", file=sys.stderr)
        return UNUSABLE
    rendered, name, address, port = argv[1], argv[2], ipaddress.ip_address(argv[3]), int(argv[4])
    protocol = argv[5] if len(argv) == 6 else "TCP"
    with open(rendered, encoding="utf-8") as handle:
        policy = policy_named(list(yaml.safe_load_all(handle)), name)
    if policy is None:
        print(f"no NetworkPolicy named {name} in {rendered}", file=sys.stderr)
        return UNUSABLE
    if egress_admits(policy, address, protocol, port):
        print(f"{name} admits {address} on {protocol}/{port}")
        return ADMITTED
    print(f"{name} denies {address} on {protocol}/{port}")
    return DENIED


if __name__ == "__main__":
    sys.exit(main(sys.argv))
