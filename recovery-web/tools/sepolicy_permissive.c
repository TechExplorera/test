/*
 * sepolicy_permissive: marca uno o varios dominios como permissive en una
 * sepolicy binaria (la /sepolicy del ramdisk del recovery).
 * Equivale a la regla "permissive <dominio>;" de la política fuente.
 *
 * Uso: sepolicy_permissive IN OUT dominio [dominio...]
 * Compilar contra libsepol estática (SELinuxProject/selinux):
 *   gcc -O2 -I libsepol/include -I libsepol/src sepolicy_permissive.c libsepol/src/libsepol.a
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sepol/policydb/policydb.h>
#include <sepol/policydb/ebitmap.h>
#include <sepol/policydb/hashtab.h>

int main(int argc, char **argv)
{
	if (argc < 4) {
		fprintf(stderr, "uso: %s IN OUT dominio [dominio...]\n", argv[0]);
		return 2;
	}
	FILE *in = fopen(argv[1], "rb");
	if (!in) { perror(argv[1]); return 1; }

	policydb_t pdb;
	struct policy_file pf;
	policy_file_init(&pf);
	pf.type = PF_USE_STDIO;
	pf.fp = in;
	if (policydb_init(&pdb) || policydb_read(&pdb, &pf, 0)) {
		fprintf(stderr, "no se pudo leer la política %s\n", argv[1]);
		return 1;
	}
	fclose(in);
	printf("política v%u, %u tipos\n", pdb.policyvers, pdb.p_types.nprim);

	for (int i = 3; i < argc; i++) {
		type_datum_t *t = hashtab_search(pdb.p_types.table, argv[i]);
		if (!t) {
			fprintf(stderr, "tipo '%s' no existe en la política\n", argv[i]);
			return 1;
		}
		if (t->flavor != TYPE_TYPE) {
			fprintf(stderr, "'%s' no es un tipo de dominio\n", argv[i]);
			return 1;
		}
		int was = ebitmap_get_bit(&pdb.permissive_map, t->s.value);
		if (ebitmap_set_bit(&pdb.permissive_map, t->s.value, 1)) {
			fprintf(stderr, "sin memoria\n");
			return 1;
		}
		t->flags |= TYPE_FLAGS_PERMISSIVE;
		printf("%s (valor %u): %s\n", argv[i], t->s.value,
		       was ? "ya era permissive" : "ahora permissive");
	}

	FILE *out = fopen(argv[2], "wb");
	if (!out) { perror(argv[2]); return 1; }
	policy_file_init(&pf);
	pf.type = PF_USE_STDIO;
	pf.fp = out;
	if (policydb_write(&pdb, &pf)) {
		fprintf(stderr, "error al escribir %s\n", argv[2]);
		return 1;
	}
	fclose(out);
	policydb_destroy(&pdb);
	return 0;
}
