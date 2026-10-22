set pagination off
break *0x451932
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/40xb *(unsigned long*)($rbp-0x30)
x/48xb $rbp-0x120
continue
