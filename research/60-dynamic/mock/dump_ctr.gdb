set pagination off
break *0x451932
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/48xb $rbp-0x120
continue
