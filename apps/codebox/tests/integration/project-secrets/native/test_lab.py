import socket, struct, unittest
from lab import dns_reply
class DNS(unittest.TestCase):
    def query(self,name,typ=1):
        labels=b''.join(bytes([len(x)])+x.encode() for x in name.split('.'))+b'\0'
        return b'\x12\x34'+struct.pack('>HHHHH',0x0100,1,0,0,0)+labels+struct.pack('>HH',typ,1)
    def test_both_hosts(self):
        # RFC 5737 documentation address: the reply only echoes the argument.
        address='192.0.2.10'
        for name in ('p02lab.test','evil.test'):
            r=dns_reply(self.query(name),address)
            self.assertEqual(r[:2],b'\x12\x34');self.assertEqual(r[-4:],socket.inet_aton(address));self.assertEqual(struct.unpack('>H',r[6:8])[0],1)
    def test_ipv6_nodata_and_unknown_refused(self):
        self.assertEqual(dns_reply(self.query('p02lab.test',28),'127.0.0.1')[3]&15,0)
        self.assertEqual(dns_reply(self.query('unknown.test'),'127.0.0.1')[3]&15,5)
    def test_malformed(self):
        self.assertIsNone(dns_reply(b'x','127.0.0.1'))
if __name__=='__main__': unittest.main()
