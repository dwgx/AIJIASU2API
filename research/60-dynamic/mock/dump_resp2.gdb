set pagination off
break *0x451937
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/48xb *(unsigned long*)($rbp-0x30)
p/x *(unsigned long*)($rbp-0x20)
continue
